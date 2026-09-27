// Package registry is a client for the Docker Registry HTTP API v2 that Nexus
// serves for each docker and oci repository: the catalog, tag lists and
// manifest descriptors (docs/architecture.md §5.5).
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/httpx"
)

// Media types of image manifests.
const (
	MediaTypeOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaTypeDockerV2       = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerV1Signed = "application/vnd.docker.distribution.manifest.v1+prettyjws"
)

// IsIndex reports whether a manifest media type is an index (a multi-platform
// image).
func IsIndex(mediaType string) bool {
	return mediaType == MediaTypeOCIIndex || mediaType == MediaTypeDockerList
}

// manifestAccept asks for any manifest, as it is stored: without an index
// type, a registry may convert or reject an index.
var manifestAccept = strings.Join([]string{
	MediaTypeOCIIndex, MediaTypeDockerList, MediaTypeOCIManifest, MediaTypeDockerV2, MediaTypeDockerV1Signed,
}, ", ")

// DefaultPageSize is the number of names or tags requested per page.
const DefaultPageSize = 1000

// Client talks to the registry endpoint of one repository.
type Client struct {
	base     *url.URL // ends with "/"; requests go to base + "v2/…"
	hc       *http.Client
	timeout  time.Duration
	pageSize int

	mu     sync.Mutex
	tokens map[string]string // scope → Bearer token
}

// New returns a client for the registry endpoint base, for example
// https://nexus.example.com/repository/docker-hosted/ or, behind a reverse
// proxy, https://registry.example.com/. timeout limits each request; zero
// means 60 seconds.
func New(base string, hc *http.Client, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errs.Config("invalid registry URL %q: use http:// or https:// with a host", base)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	if hc == nil {
		hc = http.DefaultClient
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{base: u, hc: hc, timeout: timeout, pageSize: DefaultPageSize, tokens: map[string]string{}}, nil
}

// SetPageSize sets the number of names or tags requested per page.
func (c *Client) SetPageSize(n int) {
	if n > 0 {
		c.pageSize = n
	}
}

// Endpoint returns the registry endpoint, with a trailing slash.
func (c *Client) Endpoint() string { return c.base.String() }

func (c *Client) url(path string, q url.Values) string {
	u := *c.base
	u.Path = c.base.Path + "v2/" + path
	u.RawQuery = q.Encode()
	return u.String()
}

// Catalog iterates over the image names of the repository, in the order of
// the registry (sorted).
func (c *Client) Catalog(ctx context.Context) iter.Seq2[string, error] {
	return c.paginate(ctx, "_catalog", "registry:catalog:*", func(body []byte) ([]string, error) {
		var doc struct {
			Repositories []string `json:"repositories"`
		}
		err := json.Unmarshal(body, &doc)
		return doc.Repositories, err
	})
}

// Tags iterates over the tags of an image. An unknown image is a *Error with
// status 404.
func (c *Client) Tags(ctx context.Context, image string) iter.Seq2[string, error] {
	return c.paginate(ctx, image+"/tags/list", pullScope(image), func(body []byte) ([]string, error) {
		var doc struct {
			Tags []string `json:"tags"`
		}
		err := json.Unmarshal(body, &doc)
		return doc.Tags, err
	})
}

// Descriptor describes a manifest.
type Descriptor struct {
	Digest    string
	MediaType string
	Size      int64
}

// Head returns the descriptor of the manifest that ref (a tag or a digest)
// points to. An unknown manifest is a *Error with status 404.
func (c *Client) Head(ctx context.Context, image, ref string) (Descriptor, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.do(ctx, http.MethodHead, c.url(image+"/manifests/"+ref, nil), pullScope(image))
	if err != nil {
		return Descriptor{}, err
	}
	_ = resp.Body.Close() // a HEAD response has no body
	d := Descriptor{
		Digest:    resp.Header.Get("Docker-Content-Digest"),
		MediaType: resp.Header.Get("Content-Type"),
		Size:      resp.ContentLength,
	}
	if mt, _, ok := strings.Cut(d.MediaType, ";"); ok {
		d.MediaType = strings.TrimSpace(mt)
	}
	return d, nil
}

func pullScope(image string) string { return "repository:" + image + ":pull" }

// paginate follows the pagination of the catalog and tag lists. The Link
// header only tells whether there is a next page: its URL is not followed,
// because on 3.71 it lacks the /repository/REPO prefix and behind a reverse
// proxy it may name another host. The next request is built from n and last.
func (c *Client) paginate(ctx context.Context, path, scope string, decode func([]byte) ([]string, error)) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		last := ""
		seen := map[string]bool{}
		for {
			q := url.Values{"n": {strconv.Itoa(c.pageSize)}}
			if last != "" {
				q.Set("last", last)
			}
			items, link, err := c.page(ctx, c.url(path, q), scope, decode)
			if err != nil {
				yield("", err)
				return
			}
			for _, it := range items {
				if !yield(it, nil) {
					return
				}
			}
			next, ok := nextCursor(link)
			if !ok || len(items) == 0 {
				return
			}
			if next == "" {
				next = items[len(items)-1]
			}
			if seen[next] {
				yield("", fmt.Errorf("GET %s: the registry returned the page cursor %q twice", c.url(path, nil), next))
				return
			}
			seen[next] = true
			last = next
		}
	}
}

func (c *Client) page(ctx context.Context, target, scope string, decode func([]byte) ([]string, error)) ([]string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, target, scope)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, "", c.transportError(resp.Request, err)
	}
	items, err := decode(body)
	if err != nil {
		return nil, "", fmt.Errorf("unexpected response from GET %s: %w", resp.Request.URL.Path, err)
	}
	return items, resp.Header.Get("Link"), nil
}

// nextCursor finds the rel="next" link of a Link header and returns its
// "last" parameter; ok is false when there is no next page.
func nextCursor(header string) (last string, ok bool) {
	for _, part := range strings.Split(header, ",") {
		target, params, found := strings.Cut(part, ";")
		if !found || !strings.Contains(strings.ReplaceAll(params, " ", ""), `rel="next"`) {
			continue
		}
		target = strings.Trim(strings.TrimSpace(target), "<>")
		if u, err := url.Parse(target); err == nil {
			return u.Query().Get("last"), true
		}
		return "", true
	}
	return "", false
}

// do sends a request. A 401 with a Bearer challenge (anonymous access through
// the Docker Bearer Token Realm) is answered once with a token from the realm;
// Basic credentials, when configured, are added by the HTTP client for the
// origins they belong to.
func (c *Client) do(ctx context.Context, method, target, scope string) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", manifestAccept+", application/json")
		c.mu.Lock()
		tok := c.tokens[scope]
		c.mu.Unlock()
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, c.transportError(req, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 1 {
			if ch, ok := bearerChallenge(resp.Header.Get("WWW-Authenticate")); ok {
				apiErr := decodeError(resp)
				_ = resp.Body.Close()
				tok, err := c.fetchToken(ctx, ch, scope)
				if err != nil {
					return nil, apiErr // the original answer explains more
				}
				c.mu.Lock()
				c.tokens[scope] = tok
				c.mu.Unlock()
				continue
			}
		}
		if resp.StatusCode >= 400 {
			defer resp.Body.Close()
			return nil, decodeError(resp)
		}
		return resp, nil
	}
}

type challenge struct {
	realm, service, scope string
}

// bearerChallenge parses `Bearer realm="…",service="…",scope="…"`.
func bearerChallenge(header string) (challenge, bool) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(header), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return challenge{}, false
	}
	var ch challenge
	for rest != "" {
		var key, val string
		key, rest, _ = strings.Cut(strings.TrimLeft(rest, " ,"), "=")
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				return challenge{}, false
			}
			val, rest = rest[1:end+1], rest[end+2:]
		} else {
			val, rest, _ = strings.Cut(rest, ",")
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "realm":
			ch.realm = val
		case "service":
			ch.service = val
		case "scope":
			ch.scope = val
		}
	}
	return ch, ch.realm != ""
}

func (c *Client) fetchToken(ctx context.Context, ch challenge, scope string) (string, error) {
	u, err := url.Parse(ch.realm)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid token realm %q", ch.realm)
	}
	q := u.Query()
	if ch.service != "" {
		q.Set("service", ch.service)
	}
	if ch.scope != "" {
		scope = ch.scope
	}
	q.Set("scope", scope)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", c.transportError(req, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", decodeError(resp)
	}
	var doc struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return "", fmt.Errorf("unexpected token response: %w", err)
	}
	if doc.Token == "" {
		doc.Token = doc.AccessToken
	}
	if doc.Token == "" {
		return "", errors.New("the token response contains no token")
	}
	return doc.Token, nil
}

func (c *Client) transportError(req *http.Request, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var kinded errs.Kinder
	if errors.As(err, &kinded) {
		return err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errs.Wrap(errs.KindTimeout, err, "%s %s timed out after %s", req.Method, req.URL.Path, c.timeout).
			WithHint("increase the timeout with --timeout or the \"timeout\" setting")
	case errs.IsTLSError(err):
		return errs.Wrap(errs.KindNetwork, err, "TLS verification failed for %s", req.URL.Host).
			WithHint("trust a custom CA with --ca-cert (or NEXUS_CA_CERT); --insecure disables verification")
	case errors.Is(err, context.Canceled):
		return err
	}
	return errs.Wrap(errs.KindNetwork, err, "cannot reach the registry endpoint %s", c.base).
		WithHint("check the registry URL (--registry-url, NEXR_DOCKER_REGISTRY_URL or docker.registry_urls) and your network")
}

// Error is an error response of the registry.
type Error struct {
	Method     string
	Path       string
	StatusCode int
	Code       string // registry error code, e.g. NAME_UNKNOWN
	Detail     string
	// AuthThrottled is set for "429 Too many authentication attempts".
	AuthThrottled bool
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %d %s", e.Method, e.Path, e.StatusCode, http.StatusText(e.StatusCode))
	if e.Detail != "" {
		b.WriteString(": " + e.Detail)
	}
	return b.String()
}

// HTTPStatus returns the status code.
func (e *Error) HTTPStatus() int { return e.StatusCode }

// Kind maps the status code to an error category.
func (e *Error) Kind() errs.Kind {
	switch {
	case e.AuthThrottled, e.StatusCode == http.StatusUnauthorized, e.StatusCode == http.StatusForbidden:
		return errs.KindAuth
	case e.StatusCode == http.StatusNotFound:
		return errs.KindNotFound
	case e.StatusCode == http.StatusBadRequest, e.StatusCode == http.StatusMethodNotAllowed:
		return errs.KindRejected
	}
	return errs.KindGeneric
}

// Hints suggests what to do about the error.
func (e *Error) Hints() []string {
	switch {
	case e.AuthThrottled:
		return []string{"Nexus blocks a user after repeated failed sign-ins; the block ends after 15 minutes without any request for that user"}
	case e.StatusCode == http.StatusUnauthorized:
		return []string{"check the user and password; \"nexr config view\" shows where they come from"}
	case e.StatusCode == http.StatusForbidden:
		return []string{"the user lacks the Nexus privilege needed for this operation"}
	}
	return nil
}

func decodeError(resp *http.Response) *Error {
	e := &Error{StatusCode: resp.StatusCode, AuthThrottled: httpx.AuthThrottled(resp)}
	if resp.Request != nil {
		e.Method, e.Path = resp.Request.Method, resp.Request.URL.Path
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	body = bytes.TrimSpace(body)
	var doc struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	switch {
	case len(body) > 0 && body[0] == '{' && json.Unmarshal(body, &doc) == nil && len(doc.Errors) > 0:
		e.Code = doc.Errors[0].Code
		var parts []string
		for _, re := range doc.Errors {
			parts = append(parts, strings.TrimSpace(re.Code+" "+re.Message))
		}
		e.Detail = strings.Join(parts, "; ")
	case e.AuthThrottled:
		e.Detail = "Too many authentication attempts"
	case len(body) > 0 && !strings.Contains(resp.Header.Get("Content-Type"), "html"):
		line, _, _ := strings.Cut(string(body), "\n")
		if len(line) > 300 {
			line = line[:300] + "…"
		}
		e.Detail = line
	}
	return e
}
