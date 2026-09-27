package images_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/httpx"
	"github.com/yand3r3d3v/nexr/internal/images"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/nexus/nexustest"
	"github.com/yand3r3d3v/nexr/internal/registry"
)

var t0 = time.Date(2026, 9, 26, 16, 0, 0, 0, time.UTC)

type env struct {
	fake     *nexustest.Server
	api      *nexus.Client
	svc      *images.Service
	warnings []string
}

func setup(t *testing.T, endpoint string, opts ...nexustest.Option) *env {
	t.Helper()
	fake := nexustest.New(t, opts...)
	fake.AddRepo(nexustest.Repo{Name: "docker-hosted", Format: "docker", Type: "hosted", Online: true})
	fake.AddRepo(nexustest.Repo{Name: "raw", Format: "raw", Type: "hosted", Online: true})
	hc := &http.Client{Transport: httpx.Chain(http.DefaultTransport, httpx.Options{
		Username: "admin", Password: func() (string, error) { return "admin123", nil }, AuthURLs: []string{fake.BaseURL()},
	})}
	api, err := nexus.New(fake.BaseURL(), hc, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint == "" {
		endpoint = fake.BaseURL() + "/repository/docker-hosted/"
	}
	reg, err := registry.New(strings.ReplaceAll(endpoint, "BASE", fake.BaseURL()), hc, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{fake: fake, api: api}
	e.svc, err = images.Open(context.Background(), api, reg, "docker-hosted")
	if err != nil {
		t.Fatal(err)
	}
	e.svc.Warn = func(format string, args ...any) { e.warnings = append(e.warnings, fmt.Sprintf(format, args...)) }
	return e
}

// push stores the fixture of AC-8: tags of one image pushed a minute apart,
// latest pointing to the same manifest as 1.1, and a multi-arch index.
func (e *env) push() {
	put := func(img nexustest.Image) { e.fake.PutImage("docker-hosted", img) }
	put(nexustest.Image{Name: "team/app", Tag: "1.0", Digest: "sha256:10", Pushed: t0, OS: "linux", Architecture: "amd64", TotalSize: 100})
	put(nexustest.Image{Name: "team/app", Tag: "1.1", Digest: "sha256:11", Pushed: t0.Add(time.Minute), OS: "linux", Architecture: "amd64", TotalSize: 110})
	put(nexustest.Image{Name: "team/app", Tag: "latest", Digest: "sha256:11", Pushed: t0.Add(2 * time.Minute), OS: "linux", Architecture: "amd64", TotalSize: 110})
	put(nexustest.Image{Name: "team/app", Tag: "2.0-rc", Digest: "sha256:20", Pushed: t0.Add(3 * time.Minute), Unindexed: true})
	put(nexustest.Image{Name: "multi", Tag: "1.0", Digest: "sha256:m", MediaType: nexustest.OCIIndex, Pushed: t0, OS: "linux", Architecture: "arm"})
}

func TestOpen(t *testing.T) {
	e := setup(t, "")
	ctx := context.Background()
	if _, err := images.Open(ctx, e.api, nil, "nope"); errs.Classify(err) != errs.KindNotFound || len(errs.HintsOf(err)) == 0 {
		t.Fatalf("unknown repository: %v", err)
	}
	if _, err := images.Open(ctx, e.api, nil, "raw"); errs.Classify(err) != errs.KindUsage || !strings.Contains(err.Error(), "format raw") {
		t.Fatalf("raw repository: %v", err)
	}
	if e.svc.Repository().Format != "docker" || !images.IsImageFormat("OCI") {
		t.Fatal("repository")
	}
}

func TestImagesAndStats(t *testing.T) {
	e := setup(t, "")
	e.push()
	ctx := context.Background()
	names, err := e.svc.Images(ctx)
	if err != nil || !slices.Equal(names, []string{"multi", "team/app"}) || len(e.warnings) != 0 {
		t.Fatalf("images %q, %v, %q", names, err, e.warnings)
	}
	stats, err := e.svc.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The components listing is never behind: the unindexed tag counts.
	if st := stats["team/app"]; st.Tags != 4 || !st.LastPushed.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("stats %+v", stats)
	}
}

// FR-DLS-1: the components replace a registry endpoint that fails.
func TestImagesFallback(t *testing.T) {
	e := setup(t, "BASE/repository/wrong/")
	e.push()
	names, err := e.svc.Images(context.Background())
	if err != nil || !slices.Equal(names, []string{"multi", "team/app"}) {
		t.Fatalf("images %q, %v", names, err)
	}
	if len(e.warnings) != 1 || !strings.Contains(e.warnings[0], "listing images from the components") {
		t.Fatalf("warnings %q", e.warnings)
	}
}

// AC-8, FR-DTAGS-1, FR-DTAGS-2.
func TestTags(t *testing.T) {
	e := setup(t, "")
	e.push()
	ctx := context.Background()
	tags, err := e.svc.Tags(ctx, "team/app")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]images.Tag{}
	var names []string
	for _, tg := range tags {
		byName[tg.Name] = tg
		names = append(names, tg.Name)
	}
	if !slices.Equal(names, []string{"1.0", "1.1", "2.0-rc", "latest"}) {
		t.Fatalf("tags %q", names)
	}
	one := byName["1.0"]
	if one.Digest != "sha256:10" || !one.Pushed.Equal(t0) || one.Size != 100 || one.OS != "linux" || one.ComponentID == "" ||
		one.Ref() != "team/app:1.0" || one.Repository != "docker-hosted" || one.MediaType != nexustest.OCIManifest {
		t.Fatalf("1.0 = %+v", one)
	}
	if byName["latest"].Digest != byName["1.1"].Digest || byName["latest"].Pushed.Equal(byName["1.1"].Pushed) {
		t.Fatalf("latest and 1.1 share a digest but not a push time: %+v", byName)
	}
	rc := byName["2.0-rc"]
	if rc.Digest != "sha256:20" || !rc.Pushed.IsZero() || rc.ComponentID != "" || rc.Size != -1 {
		t.Fatalf("unindexed tag %+v", rc)
	}

	multi, err := e.svc.Tags(ctx, "multi")
	if err != nil || len(multi) != 1 || !multi[0].IsIndex() || multi[0].Size != -1 || multi[0].OS != "" {
		t.Fatalf("index %+v, %v", multi, err)
	}

	if _, err := e.svc.Tags(ctx, "team/none"); errs.Classify(err) != errs.KindNotFound || !strings.Contains(err.Error(), "team/none") {
		t.Fatalf("unknown image: %v", err)
	}
}

// Without a working registry endpoint, the search index alone lists tags.
func TestTagsWithoutRegistry(t *testing.T) {
	e := setup(t, "BASE/repository/wrong/")
	e.push()
	tags, err := e.svc.Tags(context.Background(), "team/app")
	if err != nil || len(tags) != 3 || len(e.warnings) != 1 {
		t.Fatalf("tags %+v, %v, warnings %q", tags, err, e.warnings)
	}
}

func TestFindTag(t *testing.T) {
	e := setup(t, "")
	e.push()
	ctx := context.Background()
	for _, tt := range []struct {
		image, tag string
		found      bool
	}{
		{"team/app", "1.0", true},
		{"team/app", "2.0-rc", true}, // not indexed: found through the components
		{"team/app", "3.0", false},
		{"team/none", "1.0", false},
	} {
		tg, ok, err := e.svc.FindTag(ctx, tt.image, tt.tag)
		if err != nil || ok != tt.found || (ok && (tg.ComponentID == "" || tg.Name != tt.tag)) {
			t.Errorf("%s:%s = %+v, %v, %v", tt.image, tt.tag, tg, ok, err)
		}
	}
}

// FR-DRM-1, FR-DRM-5, AC-9: deleting a tag leaves the tags of the same
// manifest intact.
func TestDeleteTag(t *testing.T) {
	e := setup(t, "")
	e.push()
	ctx := context.Background()
	tg, _, err := e.svc.FindTag(ctx, "team/app", "1.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteTag(ctx, tg, true); err != nil {
		t.Fatal(err)
	}
	if got := e.fake.ImageTags("docker-hosted", "team/app"); !slices.Equal(got, []string{"1.0", "2.0-rc", "latest"}) {
		t.Fatalf("tags after deletion: %q", got)
	}
	if err := e.svc.DeleteTag(ctx, tg, false); !images.IsMissing(err) || errs.Classify(err) != errs.KindNotFound {
		t.Fatalf("second deletion: %v", err)
	}
	if err := e.svc.DeleteTag(ctx, tg, true); !images.IsMissing(err) {
		t.Fatalf("verified deletion of a missing tag: %v", err)
	}

	// A tag pushed again after the listing is not deleted when verifying.
	old, _, _ := e.svc.FindTag(ctx, "team/app", "1.0")
	e.fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: "1.0", Digest: "sha256:new"})
	if err := e.svc.DeleteTag(ctx, old, true); !errors.Is(err, images.ErrChanged) || !strings.Contains(err.Error(), "sha256:new") {
		t.Fatalf("changed tag: %v", err)
	}

	rc := images.Tag{Image: "team/app", Name: "2.0-rc"}
	if err := e.svc.DeleteTag(ctx, rc, false); err == nil || !strings.Contains(err.Error(), "search index") {
		t.Fatalf("unindexed tag: %v", err)
	}
}
