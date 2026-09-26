// Package nexustest provides an in-memory fake of the Nexus REST API for tests.
//
// It implements the endpoints nexr uses and reproduces the behaviour recorded
// in docs/nexus-api.md for the latest Nexus release. It deliberately does not
// import the nexus package, so the client's own tests can use it.
package nexustest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Repo is a repository known to the fake.
type Repo struct {
	Name   string
	Format string
	Type   string
	Online bool
	// Settings is returned by GET /v1/repositories/{format}/{type}/{name}.
	Settings map[string]any
}

type user struct {
	password string
	admin    bool
}

// Server is a fake Nexus instance.
type Server struct {
	*httptest.Server

	mu          sync.Mutex
	version     string
	edition     string
	contextPath string
	users       map[string]user
	anonymous   bool
	maxAttempts int            // failed sign-ins before the rate limit applies; 0 disables it
	failures    map[string]int // failed sign-ins per user name
	readable    bool
	writable    bool
	repos       map[string]Repo
	requests    []string
}

// Option configures a Server.
type Option func(*Server)

// WithContextPath serves the API below a context path such as "/nexus".
func WithContextPath(p string) Option {
	return func(s *Server) { s.contextPath = strings.TrimRight(p, "/") }
}

// WithAuthRateLimit sets how many failed sign-ins of a user are answered with
// 401 before further attempts get "429 Too many authentication attempts", as
// Nexus 3.96 does (default 3). Zero disables the rate limit.
func WithAuthRateLimit(maxAttempts int) Option {
	return func(s *Server) { s.maxAttempts = maxAttempts }
}

// WithVersion sets the version and edition reported in the Server header.
func WithVersion(version, edition string) Option {
	return func(s *Server) { s.version, s.edition = version, edition }
}

// New starts a fake Nexus that is closed when the test ends. It has one admin
// user "admin" with password "admin123" and anonymous access disabled.
func New(t testing.TB, opts ...Option) *Server {
	t.Helper()
	s := &Server{
		version: "3.96.3-01", edition: "COMMUNITY",
		users:    map[string]user{"admin": {password: "admin123", admin: true}},
		readable: true, writable: true,
		repos:       map[string]Repo{},
		maxAttempts: 3,
		failures:    map[string]int{},
	}
	for _, o := range opts {
		o(s)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// BaseURL returns the URL clients should use, including the context path.
func (s *Server) BaseURL() string { return s.URL + s.contextPath }

// AddUser adds or updates a user. Like an update in Nexus, it lifts the block
// of the authentication rate limit.
func (s *Server) AddUser(name, password string, admin bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[name] = user{password: password, admin: admin}
	delete(s.failures, name)
}

// SetAnonymous enables or disables anonymous access.
func (s *Server) SetAnonymous(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.anonymous = enabled
}

// SetHealth sets the results of the status endpoints.
func (s *Server) SetHealth(readable, writable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readable, s.writable = readable, writable
}

// AddRepo adds or replaces a repository.
func (s *Server) AddRepo(r Repo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos[r.Name] = r
}

// Requests returns "METHOD PATH" for every request received so far.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Server", fmt.Sprintf("Nexus/%s (%s)", s.version, s.edition))

	path, ok := strings.CutPrefix(r.URL.Path, s.contextPath+"/service/rest")
	if !ok || r.Method != http.MethodGet {
		s.htmlNotFound(w)
		return
	}
	switch {
	case path == "/v1/status":
		// Anonymous requests are allowed, wrong credentials are not.
		if _, _, has := r.BasicAuth(); has {
			if _, ok := s.authenticate(w, r); !ok {
				return
			}
		}
		s.health(w, s.readable)
	case path == "/v1/status/writable":
		s.health(w, s.writable) // credentials are ignored, even wrong ones
	case path == "/v1/repositories":
		if _, ok := s.authenticate(w, r); ok {
			s.listRepos(w)
		}
	case strings.HasPrefix(path, "/v1/repositories/"):
		u, ok := s.authenticate(w, r)
		if !ok {
			return
		}
		parts := strings.Split(strings.TrimPrefix(path, "/v1/repositories/"), "/")
		switch len(parts) {
		case 1:
			s.getRepo(w, parts[0])
		case 3:
			s.getRepoSettings(w, u, parts[0], parts[1], parts[2])
		default:
			s.siesta(w, http.StatusNotFound)
		}
	default:
		s.siesta(w, http.StatusNotFound)
	}
}

func (s *Server) health(w http.ResponseWriter, ok bool) {
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// authenticate checks Basic credentials. Wrong credentials are rejected even
// when anonymous access is enabled, as Nexus does. After more than maxAttempts
// failures the user is blocked: every request, even with the right password,
// gets 429. Nexus lifts the block after 15 idle minutes; the fake only when
// the user is updated with AddUser.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (user, bool) {
	name, pw, has := r.BasicAuth()
	if has {
		blocked := func() bool { return s.maxAttempts > 0 && s.failures[name] > s.maxAttempts }
		if blocked() {
			s.throttled(w)
			return user{}, false
		}
		if u, found := s.users[name]; found && u.password == pw {
			delete(s.failures, name)
			return u, true
		}
		if s.failures[name]++; blocked() {
			s.throttled(w)
			return user{}, false
		}
	} else if s.anonymous {
		return user{}, true
	}
	w.Header().Set("WWW-Authenticate", `BASIC realm="Sonatype Nexus Repository Manager"`)
	w.WriteHeader(http.StatusUnauthorized)
	return user{}, false
}

type repoJSON struct {
	Name       string         `json:"name"`
	Format     string         `json:"format"`
	Type       string         `json:"type"`
	URL        string         `json:"url"`
	Online     bool           `json:"online"`
	Size       int64          `json:"size"`
	Attributes map[string]any `json:"attributes"`
}

func (s *Server) repoJSON(r Repo) repoJSON {
	return repoJSON{Name: r.Name, Format: r.Format, Type: r.Type, Online: r.Online,
		URL: s.BaseURL() + "/repository/" + r.Name, Attributes: map[string]any{}}
}

func (s *Server) listRepos(w http.ResponseWriter) {
	names := make([]string, 0, len(s.repos))
	for n := range s.repos {
		names = append(names, n)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // Nexus does not sort; neither does the fake
	out := make([]repoJSON, 0, len(names))
	for _, n := range names {
		out = append(out, s.repoJSON(s.repos[n]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRepo(w http.ResponseWriter, name string) {
	r, ok := s.repos[name]
	if !ok {
		s.siesta(w, http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.repoJSON(r))
}

func (s *Server) getRepoSettings(w http.ResponseWriter, u user, format, typ, name string) {
	if !u.admin {
		s.siesta(w, http.StatusForbidden)
		return
	}
	r, ok := s.repos[name]
	apiFormat := r.Format
	if apiFormat == "maven2" {
		apiFormat = "maven"
	}
	if !ok || apiFormat != format || r.Type != typ {
		s.siesta(w, http.StatusNotFound)
		return
	}
	settings := map[string]any{"name": r.Name, "format": r.Format, "type": r.Type, "online": r.Online}
	for k, v := range r.Settings {
		settings[k] = v
	}
	writeJSON(w, http.StatusOK, settings)
}

// throttled answers like the authentication rate limiter of Nexus 3.96. Go's
// server cannot send Nexus's reason phrase, so clients must find the message
// in the body, as they must over HTTP/2.
func (s *Server) throttled(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "30")
	w.Header().Set("Content-Type", "text/html;charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte("<!DOCTYPE html><html><body><h2>Error 429 Too Many Requests</h2>" +
		"<p>Too many authentication attempts</p></body></html>"))
}

func (s *Server) siesta(w http.ResponseWriter, status int) {
	writeJSON(w, status, map[string]any{
		"status-code":    status,
		"siesta-faultid": "00000000-0000-0000-0000-000000000000",
		"status-message": strings.ToUpper(strings.ReplaceAll(http.StatusText(status), " ", "_")),
	})
}

func (s *Server) htmlNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html;charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("<!DOCTYPE html><html><body>404 - Sonatype Nexus Repository</body></html>"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
