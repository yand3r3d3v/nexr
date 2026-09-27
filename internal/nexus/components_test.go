package nexus_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/nexus/nexustest"
)

func dockerFake(t *testing.T, opts ...nexustest.Option) (*nexustest.Server, *nexus.Client) {
	t.Helper()
	fake := nexustest.New(t, opts...)
	fake.AddRepo(nexustest.Repo{Name: "docker-hosted", Format: "docker", Type: "hosted", Online: true})
	fake.AddRepo(nexustest.Repo{Name: "oci-hosted", Format: "oci", Type: "hosted", Online: true})
	return fake, client(t, fake, "admin", "admin123")
}

func TestSearchComponents(t *testing.T) {
	fake, c := dockerFake(t)
	ctx := context.Background()
	pushed := time.Date(2026, 9, 26, 16, 14, 0, 0, time.UTC)
	built := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: "1.0", Digest: "sha256:aaa", Pushed: pushed,
		Created: built, OS: "linux", Architecture: "amd64", TotalSize: 2202010})
	fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: "latest", Digest: "sha256:aaa", Pushed: pushed.Add(time.Minute),
		OS: "linux", Architecture: "amd64", TotalSize: 2202010})
	fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: "multi", MediaType: nexustest.OCIIndex,
		OS: "linux", Architecture: "arm"})
	fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: "new", Unindexed: true})
	fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app-other", Tag: "1.0"})

	got := map[string]nexus.Component{}
	for comp, err := range c.SearchComponents(ctx, nexus.ComponentQuery{Repository: "docker-hosted", Name: "team/app"}) {
		if err != nil {
			t.Fatal(err)
		}
		got[comp.Version] = comp
	}
	if len(got) != 3 || got["new"].ID != "" {
		t.Fatalf("search returned %v", got)
	}
	one := got["1.0"]
	if one.Name != "team/app" || one.Format != "docker" || len(one.Assets) != 1 {
		t.Fatalf("component %+v", one)
	}
	a := one.Assets[0]
	want := nexus.ImageInfo{Digest: "sha256:aaa", Created: built, OS: "linux", Architecture: "amd64", TotalSize: 2202010}
	if a.Path != "v2/team/app/manifests/1.0" || !a.LastModified.Equal(pushed) || a.Image == nil || *a.Image != want {
		t.Fatalf("asset %+v, image %+v", a, a.Image)
	}
	if img := got["latest"].Assets[0].Image; !img.Created.IsZero() {
		t.Errorf("a build time of year 1 must be unknown: %v", img.Created)
	}
	if img := got["multi"].Assets[0].Image; img.TotalSize != -1 || img.Architecture != "arm" {
		t.Errorf("index attributes %+v", img)
	}

	var versions []string
	for comp, err := range c.SearchComponents(ctx, nexus.ComponentQuery{Repository: "docker-hosted", Name: "team/app", Version: "latest"}) {
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, comp.Version)
	}
	if !slices.Equal(versions, []string{"latest"}) {
		t.Errorf("version search: %q", versions)
	}
}

func TestComponentsAndDelete(t *testing.T) {
	fake, c := dockerFake(t)
	ctx := context.Background()
	for i := range 23 {
		fake.PutImage("docker-hosted", nexustest.Image{Name: "app", Tag: fmt.Sprintf("v%d", i), Unindexed: i == 22})
	}
	fake.PutImage("oci-hosted", nexustest.Image{Name: "tools/bb", Tag: "1"})
	var ids []string
	for comp, err := range c.Components(ctx, "docker-hosted") {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, comp.ID)
	}
	if len(ids) != 23 {
		t.Fatalf("components listed %d tags (the listing is never behind)", len(ids))
	}
	if err := c.DeleteComponent(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteComponent(ctx, ids[0]); errs.Classify(err) != errs.KindNotFound {
		t.Fatalf("second deletion: %v", err)
	}
	if tags := fake.ImageTags("docker-hosted", "app"); len(tags) != 22 || slices.Contains(tags, "v0") {
		t.Fatalf("tags after deletion: %q", tags)
	}

	for comp, err := range c.Components(ctx, "oci-hosted") {
		if err != nil {
			t.Fatal(err)
		}
		img := comp.Assets[0].Image
		if comp.Format != "oci" || img == nil || img.Digest == "" || img.TotalSize != -1 {
			t.Fatalf("oci component %+v, image %+v", comp, img)
		}
	}
}

// Nexus 3.71 records no docker attributes; the digest comes from the checksum.
func TestComponentsWithoutImageAttributes(t *testing.T) {
	fake, c := dockerFake(t, nexustest.WithoutImageAttributes())
	fake.PutImage("docker-hosted", nexustest.Image{Name: "app", Tag: "1", Digest: "sha256:0123", OS: "linux", TotalSize: 5})
	for comp, err := range c.SearchComponents(context.Background(), nexus.ComponentQuery{Repository: "docker-hosted", Name: "app"}) {
		if err != nil {
			t.Fatal(err)
		}
		if img := comp.Assets[0].Image; *img != (nexus.ImageInfo{Digest: "sha256:0123", TotalSize: -1}) {
			t.Fatalf("image %+v", img)
		}
	}
}
