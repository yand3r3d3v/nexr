package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/yand3r3d3v/nexr/internal/httpx"
)

// ServerInfo identifies the server. Empty fields are unknown.
type ServerInfo struct {
	Header  string `json:"header"`  // Server header, e.g. "Nexus/3.96.3-01 (COMMUNITY)" or "nginx"
	Version string `json:"version"` // e.g. "3.96.3-01"
	Edition string `json:"edition"` // e.g. "COMMUNITY", "PRO", "OSS"
}

var serverRe = regexp.MustCompile(`^Nexus/(\S+)(?:\s+\(([^)]+)\))?`)

// ParseServer parses a header such as "Nexus/3.96.3-01 (COMMUNITY)".
func ParseServer(header string) ServerInfo {
	info := ServerInfo{Header: header}
	if m := serverRe.FindStringSubmatch(header); m != nil {
		info.Version, info.Edition = m[1], m[2]
	}
	return info
}

// Server identifies the server. Nexus names its version and edition in the
// Server header of every response, but reverse proxies such as nginx replace
// that header by default. The version is then read from the API description
// (/service/rest/swagger.json), which does not name the edition. The result
// is cached; a failure leaves the version empty and is returned as well.
func (c *Client) Server(ctx context.Context) (ServerInfo, error) {
	c.mu.Lock()
	info := ParseServer(c.nexusHdr)
	lastHdr, apiVersion := c.lastHdr, c.apiVersion
	c.mu.Unlock()
	if info.Version != "" {
		return info, nil
	}
	info.Header = lastHdr
	if apiVersion == "" {
		v, err := anonymousFirst(ctx, c.apiDescriptionVersion)
		if err != nil {
			return info, err
		}
		c.mu.Lock()
		c.apiVersion, apiVersion = v, v
		c.mu.Unlock()
	}
	info.Version = apiVersion
	return info, nil
}

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-\d+)?$`)

// apiDescriptionVersion reads "info.version" from the OpenAPI or Swagger
// description without reading the rest of it.
func (c *Client) apiDescriptionVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL("/service/rest/swagger.json", nil), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(io.LimitReader(resp.Body, 16<<20))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", fmt.Errorf("unexpected API description from GET %s", req.URL.Path)
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return "", err
		}
		if key != "info" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return "", err
			}
			continue
		}
		var info struct {
			Version string `json:"version"`
		}
		if err := dec.Decode(&info); err != nil {
			return "", err
		}
		if !versionRe.MatchString(info.Version) {
			return "", fmt.Errorf("unexpected version %q in the API description", info.Version)
		}
		return info.Version, nil
	}
	return "", fmt.Errorf("no version in the API description from GET %s", req.URL.Path)
}

// Health is the result of the status endpoints.
type Health struct {
	Readable bool
	Writable bool
}

// Health calls GET /v1/status and GET /v1/status/writable. Both endpoints allow
// anonymous access; 503 means "not available" and is not an error.
//
// The requests are sent without credentials, because Nexus answers 401 on
// /v1/status when the credentials are wrong, which would hide the health of
// the server. Only when a proxy in front of Nexus demands authentication are
// they repeated with credentials.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	var err error
	if h.Readable, err = c.statusEndpoint(ctx, "/v1/status"); err != nil {
		return h, err
	}
	h.Writable, err = c.statusEndpoint(ctx, "/v1/status/writable")
	return h, err
}

func (c *Client) statusEndpoint(ctx context.Context, path string) (bool, error) {
	return anonymousFirst(ctx, func(ctx context.Context) (bool, error) { return c.statusRequest(ctx, path) })
}

// anonymousFirst calls fn without credentials, and again with credentials if a
// proxy in front of Nexus demands authentication. It is used for endpoints
// that need none, so that wrong credentials neither hide the answer nor count
// as failed sign-ins for the authentication rate limit.
func anonymousFirst[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	v, err := fn(httpx.WithoutAuth(ctx))
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		return fn(ctx)
	}
	return v, err
}

func (c *Client) statusRequest(ctx context.Context, path string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	ctx = httpx.WithFinalStatus(ctx, http.StatusServiceUnavailable)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL("/service/rest"+path, nil), nil)
	if err != nil {
		return false, err
	}
	resp, err := c.Do(req)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusServiceUnavailable {
			return false, nil
		}
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return true, nil
}
