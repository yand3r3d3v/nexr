package nexus_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/httpx"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/nexus/nexustest"
)

func client(t *testing.T, fake *nexustest.Server, user, password string) *nexus.Client {
	t.Helper()
	hc := &http.Client{Transport: httpx.Chain(http.DefaultTransport, httpx.Options{
		Username: user, Password: func() (string, error) { return password, nil }, AuthURLs: []string{fake.BaseURL()},
	})}
	c, err := nexus.New(fake.BaseURL(), hc, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAgainstFake(t *testing.T) {
	fake := nexustest.New(t, nexustest.WithContextPath("/nexus"))
	fake.AddUser("reader", "pw", false)
	fake.AddRepo(nexustest.Repo{Name: "raw-hosted", Format: "raw", Type: "hosted", Online: true})
	fake.AddRepo(nexustest.Repo{Name: "maven-releases", Format: "maven2", Type: "hosted", Online: true,
		Settings: map[string]any{"storage": map[string]any{"blobStoreName": "default", "writePolicy": "ALLOW_ONCE"}}})
	ctx := context.Background()

	admin := client(t, fake, "admin", "admin123")
	repos, err := admin.Repositories(ctx)
	if err != nil || len(repos) != 2 || repos[0].Name != "maven-releases" {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
	if _, err := admin.Repository(ctx, "nope"); errs.Classify(err) != errs.KindNotFound {
		t.Fatalf("unknown repository: %v", err)
	}
	settings, err := admin.RepositorySettings(ctx, repos[0])
	if err != nil || settings["storage"] == nil {
		t.Fatalf("settings = %v, %v", settings, err)
	}

	reader := client(t, fake, "reader", "pw")
	if _, err := reader.RepositorySettings(ctx, repos[0]); errs.Classify(err) != errs.KindAuth {
		t.Fatalf("non-admin settings: %v", err)
	}

	wrong := client(t, fake, "admin", "wrong")
	if _, err := wrong.Repositories(ctx); statusOf(err) != http.StatusUnauthorized {
		t.Fatalf("wrong password: %v", err)
	}
	anon := client(t, fake, "", "")
	if _, err := anon.Repositories(ctx); statusOf(err) != http.StatusUnauthorized {
		t.Fatalf("anonymous access must be rejected by default: %v", err)
	}
	fake.SetAnonymous(true)
	if _, err := anon.Repositories(ctx); err != nil {
		t.Fatalf("anonymous enabled: %v", err)
	}
	if _, err := wrong.Repositories(ctx); statusOf(err) != http.StatusUnauthorized {
		t.Fatalf("wrong credentials must be rejected even with anonymous access: %v", err)
	}

	fake.SetHealth(true, false)
	h, err := anon.Health(ctx)
	if err != nil || !h.Readable || h.Writable {
		t.Fatalf("health = %+v %v", h, err)
	}
	if h, err := wrong.Health(ctx); err != nil || !h.Readable {
		t.Fatalf("health with wrong credentials = %+v %v", h, err)
	}
	if s := anon.Server(); s.Version != "3.96.3-01" {
		t.Fatalf("server = %+v", s)
	}
}

func TestHealthCheckIsNotRetried(t *testing.T) {
	fake := nexustest.New(t)
	fake.SetHealth(false, false)
	hc := &http.Client{Transport: httpx.Chain(http.DefaultTransport, httpx.Options{
		Retries: 3,
		Sleep: func(context.Context, time.Duration) error {
			t.Error("a 503 from a status endpoint was retried")
			return nil
		},
	})}
	c, err := nexus.New(fake.BaseURL(), hc, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.Health(context.Background())
	if err != nil || h.Readable || h.Writable {
		t.Fatalf("health = %+v, %v", h, err)
	}
	if n := len(fake.Requests()); n != 2 {
		t.Fatalf("%d requests, want 2", n)
	}
}

func statusOf(err error) int {
	var apiErr *nexus.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}
