// Package nexus is a client for the Nexus Repository 3 REST API.
//
// Behaviour of the API is documented in docs/nexus-api.md. The client returns
// typed models and *APIError values; it never assumes a page size and it
// normalises differences between Nexus releases.
package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
)

// Client talks to one Nexus instance.
type Client struct {
	base    *url.URL
	hc      *http.Client
	timeout time.Duration

	mu     sync.Mutex
	server string // last Server header seen
}

// New returns a client for the Nexus instance at baseURL (which may include a
// context path). timeout limits each API request; zero means 60 seconds.
func New(baseURL string, hc *http.Client, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, errs.Wrap(errs.KindConfig, err, "invalid Nexus URL")
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{base: u, hc: hc, timeout: timeout}, nil
}

// BaseURL returns the base URL of the instance.
func (c *Client) BaseURL() string { return c.base.String() }

// URL builds an absolute URL for a path below the base URL.
func (c *Client) URL(path string, q url.Values) string {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	u.RawPath = ""
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) recordServer(resp *http.Response) {
	if s := resp.Header.Get("Server"); strings.HasPrefix(s, "Nexus/") {
		c.mu.Lock()
		c.server = s
		c.mu.Unlock()
	}
}

// Server returns what is known about the server from its responses.
func (c *Client) Server() ServerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ParseServer(c.server)
}

// Do sends a request and returns the response. Non-2xx responses are turned
// into *APIError values and their bodies are closed.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, c.transportError(req, err)
	}
	c.recordServer(resp)
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	return resp, nil
}

// getJSON sends a GET request below /service/rest and decodes the JSON body.
func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL("/service/rest"+path, q), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return c.transportError(req, ctxErr)
		}
		return fmt.Errorf("unexpected response from GET %s: %w", req.URL.Path, err)
	}
	return nil
}

func (c *Client) transportError(req *http.Request, err error) error {
	// *url.Error repeats the method and URL; the messages below say it better.
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var kinded errs.Kinder
	if errors.As(err, &kinded) {
		return err // e.g. the password could not be read
	}
	host := req.URL.Host
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errs.Wrap(errs.KindTimeout, err, "%s %s timed out after %s", req.Method, req.URL.Path, c.timeout).
			WithHint("increase the timeout with --timeout or the \"timeout\" setting")
	case errs.IsTLSError(err):
		return errs.Wrap(errs.KindNetwork, err, "TLS verification failed for %s", host).
			WithHint("trust a custom CA with --ca-cert (or NEXUS_CA_CERT); --insecure disables verification")
	case errors.Is(err, context.Canceled):
		return err
	}
	return errs.Wrap(errs.KindNetwork, err, "cannot reach %s", host).
		WithHint("check the URL %s and your network or proxy settings (HTTPS_PROXY, NO_PROXY)", c.base)
}

type page[T any] struct {
	Items             []T     `json:"items"`
	ContinuationToken *string `json:"continuationToken"`
}

// Paginate iterates over all items of a continuation-token paginated endpoint
// below /service/rest (for example "/v1/assets").
func Paginate[T any](ctx context.Context, c *Client, path string, q url.Values) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		query := url.Values{}
		for k, v := range q {
			query[k] = append([]string(nil), v...)
		}
		seen := map[string]bool{}
		for {
			var p page[T]
			if err := c.getJSON(ctx, path, query, &p); err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range p.Items {
				if !yield(item, nil) {
					return
				}
			}
			if p.ContinuationToken == nil || *p.ContinuationToken == "" {
				return
			}
			tok := *p.ContinuationToken
			if seen[tok] {
				var zero T
				yield(zero, fmt.Errorf("GET %s returned the continuation token %q twice", path, tok))
				return
			}
			seen[tok] = true
			query.Set("continuationToken", tok)
		}
	}
}
