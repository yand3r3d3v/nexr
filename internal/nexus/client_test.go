package nexus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/httpx"
)

func newClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/", srv.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDecodeError(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		reason      string
		contentType string
		body        string
		wantErr     string
		wantKind    errs.Kind
	}{
		{
			name: "siesta fault", status: 404, contentType: "application/json",
			body:     `{"status-code":404,"siesta-faultid":"abc-123","status-message":"NOT_FOUND"}`,
			wantErr:  "GET /service/rest/v1/x: 404 Not Found (fault id abc-123)",
			wantKind: errs.KindNotFound,
		},
		{
			name: "validation array", status: 400, contentType: "application/json",
			body:     `[{"id":"*","message":"3 characters or more are required with a trailing wildcard (*)"}]`,
			wantErr:  "GET /service/rest/v1/x: 400 Bad Request: 3 characters or more are required with a trailing wildcard (*)",
			wantKind: errs.KindRejected,
		},
		{
			name: "reason phrase", status: 409, reason: "raw-once/x.txt -  cannot be updated as asset already exists and redeploy is not allowed",
			contentType: "text/plain", body: "raw-once/x.txt -  cannot be updated as asset already exists and redeploy is not allowed",
			wantErr:  "GET /service/rest/v1/x: 409 Conflict: raw-once/x.txt -  cannot be updated as asset already exists and redeploy is not allowed",
			wantKind: errs.KindRejected,
		},
		{
			name: "html page", status: 404, contentType: "text/html", body: "<html>404</html>",
			wantErr: "GET /service/rest/v1/x: 404 Not Found", wantKind: errs.KindNotFound,
		},
		{
			name: "registry error", status: 404, contentType: "application/json",
			body:     `{"errors":[{"code":"NAME_UNKNOWN","message":"repository name not known to registry"}]}`,
			wantErr:  "GET /service/rest/v1/x: 404 Not Found: NAME_UNKNOWN repository name not known to registry",
			wantKind: errs.KindNotFound,
		},
		{name: "unauthorized", status: 401, wantErr: "GET /service/rest/v1/x: 401 Unauthorized", wantKind: errs.KindAuth},
		{
			name: "auth rate limit", status: 429, reason: "Too many authentication attempts", contentType: "text/html;charset=utf-8",
			body:     "<html><body><h2>Error 429 Too Many Requests</h2><p>Too many authentication attempts</p></body></html>",
			wantErr:  "GET /service/rest/v1/x: 429 Too Many Requests: Too many authentication attempts",
			wantKind: errs.KindAuth,
		},
		{
			name: "auth rate limit without reason phrase", status: 429, contentType: "text/html;charset=utf-8",
			body:     "<html><body><h2>Error 429 Too Many Requests</h2><p>Too many authentication attempts</p></body></html>",
			wantErr:  "GET /service/rest/v1/x: 429 Too Many Requests: Too many authentication attempts",
			wantKind: errs.KindAuth,
		},
		{name: "other rate limit", status: 429, wantErr: "GET /service/rest/v1/x: 429 Too Many Requests", wantKind: errs.KindGeneric},
		{name: "server error", status: 500, body: "boom", wantErr: "GET /service/rest/v1/x: 500 Internal Server Error: boom", wantKind: errs.KindGeneric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				if tt.contentType != "" {
					w.Header().Set("Content-Type", tt.contentType)
				}
				if tt.reason != "" {
					// Reproduce Nexus's custom reason phrase on the status line.
					hj, _ := w.(http.Hijacker)
					conn, buf, _ := hj.Hijack()
					defer conn.Close()
					fmt.Fprintf(buf, "HTTP/1.1 %d %s\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
						tt.status, tt.reason, tt.contentType, len(tt.body), tt.body)
					buf.Flush()
					return
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			var out any
			err := c.getJSON(context.Background(), "/v1/x", nil, &out)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("err = %v\nwant  %s", err, tt.wantErr)
			}
			if errs.Classify(err) != tt.wantKind {
				t.Fatalf("kind = %v, want %v", errs.Classify(err), tt.wantKind)
			}
		})
	}
}

func TestPaginate(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("repository") != "raw" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		switch r.URL.Query().Get("continuationToken") {
		case "":
			fmt.Fprint(w, `{"items":[1,2],"continuationToken":"t1"}`)
		case "t1":
			fmt.Fprint(w, `{"items":[3],"continuationToken":"t2"}`)
		case "t2":
			fmt.Fprint(w, `{"items":[],"continuationToken":null}`)
		}
	})
	var got []int
	for v, err := range Paginate[int](context.Background(), c, "/v1/assets", map[string][]string{"repository": {"raw"}}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if fmt.Sprint(got) != "[1 2 3]" {
		t.Fatalf("items = %v", got)
	}

	// Stopping early must not fetch further pages.
	for v := range Paginate[int](context.Background(), c, "/v1/assets", map[string][]string{"repository": {"raw"}}) {
		if v == 1 {
			break
		}
	}
}

func TestPaginateRepeatedToken(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"items":[1],"continuationToken":"same"}`)
	})
	var lastErr error
	n := 0
	for _, err := range Paginate[int](context.Background(), c, "/v1/assets", nil) {
		if err != nil {
			lastErr = err
			break
		}
		n++
	}
	if lastErr == nil || !strings.Contains(lastErr.Error(), "twice") || n != 2 {
		t.Fatalf("n = %d err = %v", n, lastErr)
	}
}

func TestRepositoriesAndServerInfo(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "Nexus/3.96.3-01 (COMMUNITY)")
		switch r.URL.Path {
		case "/service/rest/v1/repositories":
			fmt.Fprint(w, `[{"name":"zeta","format":"raw","type":"hosted","url":"u1"},{"name":"alpha","format":"docker","type":"hosted","url":"u2","online":true}]`)
		case "/service/rest/v1/repositories/maven/hosted/maven-releases":
			fmt.Fprint(w, `{"name":"maven-releases","storage":{"blobStoreName":"default"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	repos, err := c.Repositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].Name != "alpha" || repos[0].Online == nil || !*repos[0].Online || repos[1].Online != nil {
		t.Fatalf("repos = %+v", repos)
	}
	if s, err := c.Server(context.Background()); err != nil || s.Version != "3.96.3-01" || s.Edition != "COMMUNITY" {
		t.Fatalf("server = %+v, %v", s, err)
	}
	settings, err := c.RepositorySettings(context.Background(), Repository{Name: "maven-releases", Format: "maven2", Type: "hosted"})
	if err != nil || settings["storage"] == nil {
		t.Fatalf("settings = %v, %v", settings, err)
	}
}

// Reverse proxies such as nginx replace the Server header of Nexus; the version
// then comes from the API description, read without credentials.
func TestServerBehindProxy(t *testing.T) {
	for _, tt := range []struct{ name, doc string }{
		{"OpenAPI 3 (3.96)", `{"openapi":"3.0.1","info":{"title":"Nexus Repository Manager REST API","version":"3.96.3-01"},"paths":{"/v1/x":{}}}`},
		{"Swagger 2 (3.71)", `{"swagger":"2.0","info":{"version":"3.71.0-06","title":"Nexus Repository Manager REST API"},"basePath":"/service/rest/"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var docs, withAuth int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Server", "nginx/1.27.0")
				if r.URL.Path == "/nexus/service/rest/swagger.json" {
					docs++
					if r.Header.Get("Authorization") != "" {
						withAuth++
					}
					fmt.Fprint(w, tt.doc)
					return
				}
				fmt.Fprint(w, `[]`)
			}))
			defer srv.Close()
			hc, _ := httpx.NewClient(httpx.Options{
				Username: "alice", Password: func() (string, error) { return "pw", nil }, AuthURLs: []string{srv.URL},
			})
			c, _ := New(srv.URL+"/nexus", hc, time.Second)
			if _, err := c.Repositories(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := strings.SplitN(strings.SplitN(tt.doc, `"version":"`, 2)[1], `"`, 2)[0]
			for range 2 {
				s, err := c.Server(context.Background())
				if err != nil || s.Version != want || s.Edition != "" || s.Header != "nginx/1.27.0" {
					t.Fatalf("server = %+v, %v", s, err)
				}
			}
			if docs != 1 || withAuth != 0 {
				t.Fatalf("the API description was read %d times, %d with credentials", docs, withAuth)
			}
		})
	}
}

func TestServerVersionUnavailable(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/service/rest/swagger.json" {
			fmt.Fprint(w, `{"info":{"version":"not a version"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	if s, err := c.Server(context.Background()); err == nil || s.Version != "" {
		t.Fatalf("server = %+v, %v; want an error", s, err)
	}
}

func TestParseServer(t *testing.T) {
	for in, want := range map[string]ServerInfo{
		"Nexus/3.71.0-06 (OSS)":       {Header: "Nexus/3.71.0-06 (OSS)", Version: "3.71.0-06", Edition: "OSS"},
		"Nexus/3.96.3-01 (COMMUNITY)": {Header: "Nexus/3.96.3-01 (COMMUNITY)", Version: "3.96.3-01", Edition: "COMMUNITY"},
		"Nexus/3.90.0-01":             {Header: "Nexus/3.90.0-01", Version: "3.90.0-01"},
		"nginx":                       {Header: "nginx"},
	} {
		if got := ParseServer(in); got != want {
			t.Errorf("ParseServer(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestHealth(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/service/rest/v1/status":
			w.WriteHeader(http.StatusOK)
		case "/service/rest/v1/status/writable":
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	h, err := c.Health(context.Background())
	if err != nil || !h.Readable || h.Writable {
		t.Fatalf("health = %+v, %v", h, err)
	}
}

// A proxy in front of Nexus may demand authentication even for the status
// endpoints; the anonymous health check is then repeated with credentials.
func TestHealthBehindAuthenticatingProxy(t *testing.T) {
	var anonymous, authenticated int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			anonymous++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated++
	}))
	defer srv.Close()
	hc, err := httpx.NewClient(httpx.Options{
		Username: "alice", AuthURLs: []string{srv.URL},
		Password: func() (string, error) { return "s3cret", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(srv.URL, hc, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.Health(context.Background())
	if err != nil || !h.Readable || !h.Writable || anonymous != 2 || authenticated != 2 {
		t.Fatalf("health = %+v, %v; %d anonymous and %d authenticated requests", h, err, anonymous, authenticated)
	}
}

func TestTransportErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here any more
	c, _ := New("http://"+addr, &http.Client{}, time.Second)
	_, err = c.Repositories(context.Background())
	if errs.Classify(err) != errs.KindNetwork || len(errs.HintsOf(err)) == 0 {
		t.Fatalf("refused: err = %v kind %v", err, errs.Classify(err))
	}

	slow := newClient(t, func(http.ResponseWriter, *http.Request) {
		time.Sleep(300 * time.Millisecond)
	})
	slow.timeout = 50 * time.Millisecond
	_, err = slow.Repositories(context.Background())
	if errs.Classify(err) != errs.KindTimeout || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: err = %v kind %v", err, errs.Classify(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = slow.Repositories(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: err = %v", err)
	}
}

func TestURLWithContextPath(t *testing.T) {
	c, _ := New("https://example.com/nexus/", nil, 0)
	if got := c.URL("/service/rest/v1/repositories", map[string][]string{"a": {"b c"}}); got != "https://example.com/nexus/service/rest/v1/repositories?a=b+c" {
		t.Fatalf("URL = %q", got)
	}
}
