package nexus

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/yand3r3d3v/nexr/internal/httpx"
)

// ServerInfo is what the Server response header reveals about the instance.
type ServerInfo struct {
	Header  string `json:"header"`
	Version string `json:"version"`
	Edition string `json:"edition"`
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
	ok, err := c.statusRequest(httpx.WithoutAuth(ctx), path)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		return c.statusRequest(ctx, path)
	}
	return ok, err
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
