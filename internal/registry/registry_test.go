package registry_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/httpx"
	"github.com/yand3r3d3v/nexr/internal/nexus/nexustest"
	"github.com/yand3r3d3v/nexr/internal/registry"
)

func httpClient(user, password string, authURLs ...string) *http.Client {
	return &http.Client{Transport: httpx.Chain(http.DefaultTransport, httpx.Options{
		Username: user, Password: func() (string, error) { return password, nil }, AuthURLs: authURLs,
	})}
}

func collect(t *testing.T, seq func(func(string, error) bool)) ([]string, error) {
	t.Helper()
	var out []string
	for v, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, v)
	}
	return out, nil
}

func fakeRegistry(t *testing.T, opts ...nexustest.Option) (*nexustest.Server, *registry.Client) {
	t.Helper()
	fake := nexustest.New(t, opts...)
	fake.AddRepo(nexustest.Repo{Name: "docker-hosted", Format: "docker", Type: "hosted", Online: true})
	for i := range 5 {
		fake.PutImage("docker-hosted", nexustest.Image{Name: fmt.Sprintf("team/app%d", i), Tag: "1.0"})
	}
	for _, tag := range []string{"1.0", "1.1", "2.0", "latest", "V1"} {
		fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: tag})
	}
	c, err := registry.New(fake.BaseURL()+"/repository/docker-hosted", httpClient("admin", "admin123", fake.BaseURL()), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return fake, c
}

func TestCatalogAndTags(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			var opts []nexustest.Option
			if legacy {
				opts = append(opts, nexustest.WithLegacyRegistryLinks())
			}
			fake, c := fakeRegistry(t, opts...)
			c.SetPageSize(2)
			if c.Endpoint() != fake.BaseURL()+"/repository/docker-hosted/" {
				t.Fatalf("endpoint %s", c.Endpoint())
			}
			ctx := context.Background()
			names, err := collect(t, c.Catalog(ctx))
			want := []string{"team/app", "team/app0", "team/app1", "team/app2", "team/app3", "team/app4"}
			if err != nil || !slices.Equal(names, want) {
				t.Fatalf("catalog %q, %v", names, err)
			}
			tags, err := collect(t, c.Tags(ctx, "team/app"))
			if err != nil || !slices.Equal(tags, []string{"1.0", "1.1", "2.0", "V1", "latest"}) {
				t.Fatalf("tags %q, %v", tags, err)
			}
			// Every request stays below the repository path, whatever the Link says.
			for _, r := range fake.Requests() {
				if !strings.Contains(r, "/repository/docker-hosted/v2/") {
					t.Errorf("request outside the endpoint: %s", r)
				}
			}
		})
	}
}

func TestTagsOfUnknownImage(t *testing.T) {
	_, c := fakeRegistry(t)
	_, err := collect(t, c.Tags(context.Background(), "team/none"))
	var regErr *registry.Error
	if errs.Classify(err) != errs.KindNotFound || !asError(err, &regErr) || regErr.Code != "NAME_UNKNOWN" {
		t.Fatalf("unknown image: %v", err)
	}
}

func asError(err error, target **registry.Error) bool {
	e, ok := err.(*registry.Error) //nolint:errorlint // returned unwrapped
	*target = e
	return ok
}

func TestHead(t *testing.T) {
	fake, c := fakeRegistry(t)
	fake.PutImage("docker-hosted", nexustest.Image{Name: "multi", Tag: "1.0", Digest: "sha256:beef", MediaType: nexustest.OCIIndex})
	ctx := context.Background()
	d, err := c.Head(ctx, "multi", "1.0")
	if err != nil || d.Digest != "sha256:beef" || d.MediaType != registry.MediaTypeOCIIndex || !registry.IsIndex(d.MediaType) || d.Size <= 0 {
		t.Fatalf("head %+v, %v", d, err)
	}
	if d, err := c.Head(ctx, "multi", "sha256:beef"); err != nil || d.Digest != "sha256:beef" {
		t.Fatalf("head by digest %+v, %v", d, err)
	}
	if _, err := c.Head(ctx, "multi", "2.0"); errs.Classify(err) != errs.KindNotFound {
		t.Fatalf("unknown tag: %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	fake, _ := fakeRegistry(t)
	c, err := registry.New(fake.BaseURL()+"/repository/docker-hosted/", httpClient("admin", "wrong", fake.BaseURL()), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collect(t, c.Catalog(context.Background()))
	if errs.Classify(err) != errs.KindAuth || len(errs.HintsOf(err)) == 0 {
		t.Fatalf("wrong password: %v", err)
	}
}

// Anonymous clients of a registry with the Bearer token realm get a token.
func TestAnonymousBearerToken(t *testing.T) {
	fake, _ := fakeRegistry(t, nexustest.WithBearerTokenRealm())
	anon, err := registry.New(fake.BaseURL()+"/repository/docker-hosted/", httpClient("", ""), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := collect(t, anon.Catalog(ctx)); errs.Classify(err) != errs.KindAuth {
		t.Fatalf("anonymous access disabled: %v", err)
	}
	fake.SetAnonymous(true)
	if names, err := collect(t, anon.Catalog(ctx)); err != nil || len(names) != 6 {
		t.Fatalf("anonymous catalog %q, %v", names, err)
	}
	if tags, err := collect(t, anon.Tags(ctx, "team/app")); err != nil || len(tags) != 5 {
		t.Fatalf("anonymous tags %q, %v", tags, err)
	}
	// The token of each scope is fetched once.
	n := 0
	for _, r := range fake.Requests() {
		if strings.HasSuffix(r, "/v2/token") {
			n++
		}
	}
	if _, err := collect(t, anon.Tags(ctx, "team/app")); err != nil {
		t.Fatal(err)
	}
	m := 0
	for _, r := range fake.Requests() {
		if strings.HasSuffix(r, "/v2/token") {
			m++
		}
	}
	if n != 3 || m != n { // catalog: 1 failed + 1 successful, tags: 1
		t.Fatalf("token requests: %d, then %d", n, m)
	}

	// With credentials, Basic authentication is used directly.
	c, err := registry.New(fake.BaseURL()+"/repository/docker-hosted/", httpClient("admin", "admin123", fake.BaseURL()), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if names, err := collect(t, c.Catalog(ctx)); err != nil || len(names) != 6 {
		t.Fatalf("catalog with credentials %q, %v", names, err)
	}
}

// Links that lack the cursor, or name another host, only signal a next page.
func TestPaginationLinks(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Query().Get("last") {
		case "":
			w.Header().Set("Link", `<https://elsewhere.example.com/v2/_catalog?n=2>; rel="next"`)
			fmt.Fprint(w, `{"repositories": ["a", "b"]}`)
		case "b":
			w.Header().Set("Link", `<https://elsewhere.example.com/v2/_catalog?n=2&last=c>; rel="next"`)
			fmt.Fprint(w, `{"repositories": ["c"]}`)
		case "c":
			fmt.Fprint(w, `{"repositories": ["d"]}`)
		}
	}))
	defer srv.Close()
	c, err := registry.New(srv.URL+"/proxy", srv.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetPageSize(2)
	names, err := collect(t, c.Catalog(context.Background()))
	if err != nil || !slices.Equal(names, []string{"a", "b", "c", "d"}) || requests.Load() != 3 {
		t.Fatalf("catalog %q, %v after %d requests", names, err, requests.Load())
	}
}

func TestPaginationLoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", `</v2/_catalog?n=1&last=a>; rel="next"`)
		fmt.Fprint(w, `{"repositories": ["a"]}`)
	}))
	defer srv.Close()
	c, err := registry.New(srv.URL, srv.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collect(t, c.Catalog(context.Background())); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("loop: %v", err)
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/_catalog":
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "Repository not found")
		case "/v2/app/tags/list":
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, "<html>bad gateway</html>")
		}
	}))
	defer srv.Close()
	c, err := registry.New(srv.URL, srv.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = collect(t, c.Catalog(ctx))
	if errs.Classify(err) != errs.KindNotFound || !strings.Contains(err.Error(), "Repository not found") {
		t.Fatalf("catalog: %v", err)
	}
	if _, err := collect(t, c.Tags(ctx, "app")); errs.Classify(err) != errs.KindGeneric || !strings.Contains(err.Error(), "502") {
		t.Fatalf("tags: %v", err)
	}

	for _, bad := range []string{"registry.example.com", "ftp://x", "http://"} {
		if _, err := registry.New(bad, nil, 0); errs.Classify(err) != errs.KindConfig {
			t.Errorf("%s: %v", bad, err)
		}
	}
	unreachable, _ := registry.New("http://127.0.0.1:1/", nil, time.Second)
	if _, err := collect(t, unreachable.Catalog(ctx)); errs.Classify(err) != errs.KindNetwork {
		t.Fatalf("unreachable: %v", err)
	}
}
