package root_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/nexus/nexustest"
)

// dockerFixture has a docker repository with the tags of the FR-DRM-2
// example (latest, v5 … v1, pushed a day apart, latest newest), a multi-arch
// image and an oci repository.
func dockerFixture(t *testing.T, opts ...nexustest.Option) (*nexustest.Server, invocation) {
	t.Helper()
	fake := nexustest.New(t, opts...)
	fake.AddRepo(nexustest.Repo{Name: "docker-hosted", Format: "docker", Type: "hosted", Online: true})
	fake.AddRepo(nexustest.Repo{Name: "oci-hosted", Format: "oci", Type: "hosted", Online: true})
	fake.AddRepo(nexustest.Repo{Name: "raw", Format: "raw", Type: "hosted", Online: true})
	now := time.Now().UTC().Truncate(time.Millisecond)
	day := 24 * time.Hour
	for i, v := range []string{"v1", "v2", "v3", "v4", "v5"} {
		fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: v, Digest: "sha256:" + strings.Repeat(v[1:], 64),
			Pushed: now.Add(time.Duration(i-6) * day), OS: "linux", Architecture: "amd64", TotalSize: int64(1000 * (i + 1))})
	}
	fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: "latest", Digest: "sha256:" + strings.Repeat("5", 64),
		Pushed: now.Add(-time.Hour), OS: "linux", Architecture: "amd64", TotalSize: 5000})
	fake.PutImage("docker-hosted", nexustest.Image{Name: "team/multi", Tag: "1.0", MediaType: nexustest.OCIIndex,
		Pushed: now.Add(-day), OS: "linux", Architecture: "arm"})
	fake.PutImage("docker-hosted", nexustest.Image{Name: "team/multi", Tag: "amd64", Pushed: now.Add(-day)})
	fake.PutImage("oci-hosted", nexustest.Image{Name: "tools/bb", Tag: "1", Pushed: now.Add(-day)})
	inv := admin(t, fake)
	inv.env["NEXR_DOCKER_REPO"] = "docker-hosted"
	return fake, inv
}

func TestDockerLs(t *testing.T) {
	fake, inv := dockerFixture(t)
	r := inv.run(t, "docker", "ls")
	mustExit(t, r, 0)
	if r.stdout != "team/app\nteam/multi\n" {
		t.Fatalf("docker ls\n%s", r)
	}
	r = inv.run(t, "docker", "images", "-R", "oci-hosted")
	if r.stdout != "tools/bb\n" {
		t.Fatalf("docker ls -R\n%s", r)
	}
	r = inv.run(t, "docker", "ls", "-l", "--match", "*app")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "IMAGE     TAGS  LAST PUSHED\nteam/app  6     ")
	if strings.Contains(r.stdout, "multi") {
		t.Fatalf("--match\n%s", r)
	}
	r = inv.run(t, "docker", "ls", "-l", "--json")
	list := decode[[]map[string]any](t, r, r.stdout)
	if len(list) != 2 || list[0]["tag_count"] != 6.0 || list[0]["last_pushed"] == nil || list[1]["repository"] != "docker-hosted" {
		t.Fatalf("docker ls -l --json\n%s", r)
	}
	r = inv.run(t, "docker", "ls", "--json")
	if list := decode[[]map[string]any](t, r, r.stdout); list[0]["tag_count"] != nil {
		t.Fatalf("tag_count without -l\n%s", r)
	}

	noRepo := admin(t, fake)
	r = noRepo.run(t, "docker", "ls")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "no Docker repository selected")
	mustContain(t, r, "stderr", "Docker repositories: docker-hosted, oci-hosted")
	r = inv.run(t, "docker", "ls", "-R", "raw")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "repository raw has the format raw")
	r = inv.run(t, "docker", "ls", "-R", "nope")
	mustExit(t, r, 5)
	r = inv.run(t, "docker", "lst")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", `did you mean "nexr docker ls"?`)
}

// AC-8: digests, push times and sizes, also for indexes and for tags that are
// not in the search index yet.
func TestDockerTags(t *testing.T) {
	fake, inv := dockerFixture(t)
	fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: "v6-rc", Digest: "sha256:66", Unindexed: true})

	r := inv.run(t, "docker", "tags", "team/app")
	mustExit(t, r, 0)
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	var order []string
	for _, l := range lines[1:] {
		order = append(order, strings.Fields(l)[0])
	}
	// Newest push first; the unindexed tag was pushed a moment ago.
	if !slices.Equal(order, []string{"v6-rc", "latest", "v5", "v4", "v3", "v2", "v1"}) || !strings.HasPrefix(lines[0], "TAG") {
		t.Fatalf("docker tags\n%s", r)
	}
	mustContain(t, r, "stdout", "sha256:555555555555  ")
	mustContain(t, r, "stdout", "4.9 KiB")
	mustContain(t, r, "stdout", "v6-rc   sha256:66            -")

	r = inv.run(t, "docker", "tags", "team/app", "--sort", "semver", "-q")
	if r.stdout != "v6-rc\nv5\nv4\nv3\nv2\nv1\nlatest\n" { // v6-rc is 6.0.0-rc
		t.Fatalf("--sort semver\n%s", r)
	}
	r = inv.run(t, "docker", "tags", "team/app", "--sort", "name", "--reverse", "-q", "--match", "v*")
	if r.stdout != "v6-rc\nv5\nv4\nv3\nv2\nv1\n" {
		t.Fatalf("--sort name --reverse --match\n%s", r)
	}
	r = inv.run(t, "docker", "tags", "team/multi", "-l")
	// Nexus records the platform of one child of an index only: none is shown.
	mustContain(t, r, "stdout", "1.0    sha256:86d43d46aa37  ")
	mustContain(t, r, "stdout", "multi-arch  index  -  ")

	r = inv.run(t, "docker", "tags", "team/app:latest", "--json")
	mustExit(t, r, 0)
	tags := decode[[]map[string]any](t, r, r.stdout)
	if len(tags) != 1 || tags[0]["tag"] != "latest" || tags[0]["size"] != 5000.0 || tags[0]["os"] != "linux" ||
		tags[0]["component_id"] == nil || tags[0]["image"] != "team/app" || tags[0]["pushed"] == nil {
		t.Fatalf("tags --json\n%s", r)
	}
	r = inv.run(t, "docker", "tags", "team/multi:1.0", "--json")
	if tags := decode[[]map[string]any](t, r, r.stdout); tags[0]["size"] != nil || tags[0]["os"] != nil {
		t.Fatalf("an index has no size or platform\n%s", r)
	}
	r = inv.run(t, "docker", "tags", "team/app:v6-rc", "--json")
	if tags := decode[[]map[string]any](t, r, r.stdout); tags[0]["pushed"] != nil || tags[0]["digest"] != "sha256:66" {
		t.Fatalf("unindexed tag\n%s", r)
	}

	r = inv.run(t, "docker", "tags", "team/app:v9")
	mustExit(t, r, 5)
	r = inv.run(t, "docker", "tags", "team/none")
	mustExit(t, r, 5)
	mustContain(t, r, "stderr", "image team/none not found in docker-hosted")
	r = inv.run(t, "docker", "tags", "Team/App")
	mustExit(t, r, 2)
	r = inv.run(t, "docker", "tags", "team/app", "--sort", "size")
	mustExit(t, r, 2)
}

// FR-DRM-1: explicit tags; other tags of the same manifest stay.
func TestDockerRmTags(t *testing.T) {
	fake, inv := dockerFixture(t)
	tags := func() []string { return fake.ImageTags("docker-hosted", "team/app") }

	r := inv.run(t, "docker", "rm", "team/app:v5", "--dry-run")
	mustExit(t, r, 0)
	if r.stdout != "would delete  docker-hosted/team/app:v5\n1 tag would be deleted (dry run)\n" {
		t.Fatalf("dry run\n%s", r)
	}
	r = inv.run(t, "docker", "rm", "team/app:v5")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "deleted  docker-hosted/team/app:v5\n")
	mustContain(t, r, "stderr", "hint: storage is reclaimed")
	if got := tags(); !slices.Equal(got, []string{"latest", "v1", "v2", "v3", "v4"}) {
		t.Fatalf("tags %q: latest has the digest of v5 and must stay", got)
	}

	r = inv.run(t, "docker", "rm", "team/app:v5")
	mustExit(t, r, 5)
	mustContain(t, r, "stderr", "docker-hosted/team/app:v5 not found")
	r = inv.run(t, "docker", "rm", "team/app:v5", "--dry-run")
	mustExit(t, r, 0)
	mustContain(t, r, "stderr", "failed    docker-hosted/team/app:v5: not found")
	r = inv.run(t, "docker", "rm", "team/app:v5", "--ignore-missing", "--json")
	mustExit(t, r, 0)
	doc := decode[map[string]any](t, r, r.stdout)
	if len(doc["missing"].([]any)) != 1 || doc["dry_run"] != false {
		t.Fatalf("--ignore-missing --json\n%s", r)
	}

	r = inv.run(t, "docker", "rm", "team/app:v4", "team/app:v3")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "refusing to delete 2 tags without confirmation")
	r = invocation{env: inv.env, stdin: "y\n", tty: true}.run(t, "docker", "rm", "team/app:v4", "team/app:v3", "team/app:v3")
	mustExit(t, r, 0)
	mustContain(t, r, "stderr", "This deletes 2 tags:\n  docker-hosted/team/app:v4\n")
	mustContain(t, r, "stdout", "2 tags deleted in")
	r = inv.run(t, "docker", "rm", "team/app:v2", "team/app:nope", "--yes", "--json")
	mustExit(t, r, 6)
	doc = decode[map[string]any](t, r, r.stdout)
	if len(doc["deleted"].([]any)) != 1 || len(doc["failed"].([]any)) != 1 {
		t.Fatalf("partial failure\n%s", r)
	}
	if got := tags(); !slices.Equal(got, []string{"latest", "v1"}) {
		t.Fatalf("tags %q", got)
	}

	r = inv.run(t, "docker", "rm", "team/app")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "team/app has no tag")
	r = inv.run(t, "docker", "rm", "team/app:v1", "--exclude", "v1")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "--exclude needs a retention policy")
}

// FR-DRM-2 to FR-DRM-6, AC-9.
func TestDockerRetention(t *testing.T) {
	fake, inv := dockerFixture(t)
	tags := func() []string { return fake.ImageTags("docker-hosted", "team/app") }

	r := inv.run(t, "docker", "rm", "team/app", "--keep", "2", "--dry-run")
	mustExit(t, r, 0)
	for _, want := range []string{
		"TAG     PUSHED            ACTION  REASON\nlatest  ",
		"keep    protected (latest)\nv5      ",
		"keep    newest 2\nv3      ",
		"delete  beyond newest 2\nv2",
		"dry run: 3 of 6 tags would be deleted from docker-hosted/team/app\n",
	} {
		mustContain(t, r, "stdout", want)
	}
	if len(tags()) != 6 {
		t.Fatal("a dry run must not delete")
	}
	r = inv.run(t, "docker", "rm", "team/app", "--keep", "2", "--dry-run", "--json")
	doc := decode[map[string]any](t, r, r.stdout)
	plans := doc["images"].([]any)
	decisions := plans[0].(map[string]any)["decisions"].([]any)
	if len(decisions) != 6 || len(doc["deleted"].([]any)) != 3 || doc["dry_run"] != true {
		t.Fatalf("dry run JSON\n%s", r)
	}
	if d := decisions[3].(map[string]any); d["tag"] != "v3" || d["action"] != "delete" || d["reason"] != "beyond newest 2" || d["pushed"] == nil {
		t.Fatalf("decision %v", d)
	}

	// Retention always asks: without a terminal, --yes is required.
	r = inv.run(t, "docker", "rm", "team/app", "--keep", "2")
	mustExit(t, r, 2)
	mustContain(t, r, "stdout", "3 of 6 tags to delete from docker-hosted/team/app")
	mustContain(t, r, "stderr", "refusing to delete 3 tags without confirmation")
	r = invocation{env: inv.env, stdin: "n\n", tty: true}.run(t, "docker", "rm", "team/app", "--keep", "2")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "Delete 3 tags? [y/N]")

	r = inv.run(t, "docker", "rm", "team/app", "--keep", "2", "--yes")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "3 tags deleted in")
	if got := tags(); !slices.Equal(got, []string{"latest", "v4", "v5"}) {
		t.Fatalf("after --keep 2: %q", got)
	}
	r = inv.run(t, "docker", "rm", "team/app", "--keep", "2", "--yes")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "nothing to delete")

	// --older-than keeps recent pushes; --all with --match; the image of the
	// multi-arch index is untouched.
	r = inv.run(t, "docker", "rm", "team/app", "--older-than", "1d", "--yes", "-q")
	mustExit(t, r, 0)
	if r.stdout != "docker-hosted/team/app:v4\n" && r.stdout != "docker-hosted/team/app:v5\ndocker-hosted/team/app:v4\n" &&
		r.stdout != "docker-hosted/team/app:v4\ndocker-hosted/team/app:v5\n" {
		t.Fatalf("--older-than -q\n%s", r)
	}
	if got := tags(); !slices.Equal(got, []string{"latest"}) {
		t.Fatalf("after --older-than: %q", got)
	}
	if got := fake.ImageTags("docker-hosted", "team/multi"); len(got) != 2 {
		t.Fatalf("team/multi: %q", got)
	}
}

func TestDockerRetentionOptions(t *testing.T) {
	fake, inv := dockerFixture(t)
	fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: "nightly", Pushed: time.Now().Add(-10 * 24 * time.Hour)})
	fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: "v7", Unindexed: true})

	r := inv.run(t, "docker", "rm", "team/app", "--keep", "1", "--sort", "semver", "--dry-run", "--json")
	mustExit(t, r, 0)
	doc := decode[map[string]any](t, r, r.stdout)
	actions := map[string]string{}
	for _, d := range doc["images"].([]any)[0].(map[string]any)["decisions"].([]any) {
		m := d.(map[string]any)
		actions[m["tag"].(string)] = m["action"].(string) + ": " + m["reason"].(string)
	}
	want := map[string]string{
		"v7": "keep: not in the search index yet", "v5": "keep: highest 1", "v4": "delete: beyond highest 1",
		"v1": "delete: beyond highest 1", "latest": "keep: protected (latest)", "nightly": "skip: not a version",
		"v2": "delete: beyond highest 1", "v3": "delete: beyond highest 1",
	}
	for tag, w := range want {
		if actions[tag] != w {
			t.Errorf("%s: %q, want %q", tag, actions[tag], w)
		}
	}
	if s := doc["summary"].(map[string]any); s["kept"] != 3.0 || s["skipped"] != 1.0 || s["deleted"] != 4.0 {
		t.Errorf("summary %v", s)
	}

	// Patterns of images; --all; --exclude adds to docker.exclude.
	r = inv.run(t, "docker", "rm", "team/*", "--all", "--exclude", "v5", "--match", "v*", "--dry-run", "-q")
	mustExit(t, r, 0)
	if r.stdout != "docker-hosted/team/app:v4\ndocker-hosted/team/app:v3\ndocker-hosted/team/app:v2\ndocker-hosted/team/app:v1\n" {
		t.Fatalf("pattern --all\n%s", r)
	}
	r = inv.run(t, "docker", "rm", "team/app", "team/*", "--keep", "1", "--dry-run", "--json")
	if doc := decode[map[string]any](t, r, r.stdout); len(doc["images"].([]any)) != 2 {
		t.Fatalf("an image named twice is planned once\n%s", r)
	}
	r = inv.run(t, "docker", "rm", "nothing/*", "--all", "--dry-run")
	mustExit(t, r, 0)
	mustContain(t, r, "stderr", "no image in docker-hosted matches nothing/*")

	for _, tt := range []struct {
		args []string
		msg  string
	}{
		{[]string{"team/app", "--keep", "0"}, "keep at least 1 tag"},
		{[]string{"team/app", "--keep", "2", "--all"}, "cannot be used together"},
		{[]string{"team/app", "--older-than", "soon"}, "invalid --older-than"},
		{[]string{"team/app:v1", "--keep", "2"}, "names a tag, but a retention policy applies to whole images"},
		{[]string{"team/app", "--keep", "2", "--sort", "size"}, "invalid sort order"},
	} {
		r := inv.run(t, append([]string{"docker", "rm"}, tt.args...)...)
		mustExit(t, r, 2)
		mustContain(t, r, "stderr", tt.msg)
	}

	// docker.exclude from the config file replaces the default ["latest"].
	cfg := withConfig(t, "docker:\n  exclude: []\n", "NEXUS_URL", fake.BaseURL(), "NEXUS_USER", "admin",
		"NEXUS_PASSWORD", "admin123", "NEXR_DOCKER_REPO", "docker-hosted")
	r = cfg.run(t, "docker", "rm", "team/app", "--keep", "1", "--dry-run", "-q")
	mustContain(t, r, "stdout", "docker-hosted/team/app:v5\n") // latest is the newest and no longer protected
	if strings.Contains(r.stdout, ":latest") {
		t.Fatalf("latest counts as the newest tag\n%s", r)
	}
}

// FR-NET-3, FR-IMGREF-3: a reverse proxy serves the registry at the root of
// another host, which gets the credentials.
func TestDockerRegistryURL(t *testing.T) {
	fake, inv := dockerFixture(t)
	target, _ := url.Parse(fake.BaseURL())
	var withAuth, requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if _, _, ok := r.BasicAuth(); ok {
			withAuth.Add(1)
		}
		rp := httputil.NewSingleHostReverseProxy(target)
		r.URL.Path = "/repository/docker-hosted" + r.URL.Path
		rp.ServeHTTP(w, r)
	}))
	defer proxy.Close()

	r := inv.run(t, "docker", "tags", "team/app", "--registry-url", proxy.URL, "-q")
	mustExit(t, r, 0)
	if requests.Load() == 0 || withAuth.Load() != requests.Load() || !strings.Contains(r.stdout, "v1\n") {
		t.Fatalf("%d requests to the registry URL, %d with credentials\n%s", requests.Load(), withAuth.Load(), r)
	}

	host := strings.TrimPrefix(proxy.URL, "http://")
	// The endpoint belongs to the URL of the same source (credential scoping).
	cfg := withConfig(t, "url: "+fake.BaseURL()+"\ndocker:\n  registry_urls:\n    docker-hosted: "+proxy.URL+"\n",
		"NEXUS_USER", "admin", "NEXUS_PASSWORD", "admin123")
	before := requests.Load()
	r = cfg.run(t, "docker", "tags", host+"/team/app:latest", "-q")
	mustExit(t, r, 0)
	if r.stdout != "latest\n" || requests.Load() == before {
		t.Fatalf("image reference with the registry host\n%s", r)
	}
	r = cfg.run(t, "docker", "tags", "registry.example.com/team/app")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "matches no configured registry URL")
	r = cfg.run(t, "docker", "tags", "team/app")
	mustExit(t, r, 2) // no repository selected

	// With the Nexus URL from another source, the endpoint may belong to
	// another server and is ignored.
	other := withConfig(t, "docker:\n  registry_urls:\n    docker-hosted: "+proxy.URL+"\n",
		"NEXUS_URL", fake.BaseURL(), "NEXUS_USER", "admin", "NEXUS_PASSWORD", "admin123")
	r = other.run(t, "docker", "tags", host+"/team/app")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "ignored the registry URL "+proxy.URL+" of docker-hosted from file")

	r = inv.run(t, "docker", "tags", "team/app", "--registry-url", "registry.example.com")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "invalid --registry-url")

	// FR-DLS-1: a failing registry endpoint falls back to the components.
	r = inv.run(t, "docker", "ls", "--registry-url", fake.BaseURL()+"/repository/wrong/")
	mustExit(t, r, 0)
	if r.stdout != "team/app\nteam/multi\n" {
		t.Fatalf("fallback\n%s", r)
	}
	mustContain(t, r, "stderr", "listing images from the components instead")
}

// FR-DRM-4: between the plan and the deletion, a tag may be pushed again (it
// is skipped), deleted by someone else (it is missing) or refused (it fails).
func TestDockerRetentionRaces(t *testing.T) {
	var fake *nexustest.Server
	var once sync.Map
	fake, inv := dockerFixture(t, nexustest.WithHook(func(r *http.Request) {
		if r.Method != http.MethodHead {
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/team/app/manifests/v1"):
			if _, done := once.LoadOrStore("v1", true); !done {
				fake.PutImage("docker-hosted", nexustest.Image{Name: "team/app", Tag: "v1", Digest: "sha256:again"})
			}
		case strings.HasSuffix(r.URL.Path, "/team/app/manifests/v2"):
			fake.DeleteImage("docker-hosted", "team/app", "v2")
		}
	}))
	inv.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "/v1/components/") {
			if _, done := once.LoadOrStore("delete", true); done {
				return http.DefaultTransport.RoundTrip(req)
			}
			return &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Header: http.Header{},
				Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		return http.DefaultTransport.RoundTrip(req)
	})

	r := inv.run(t, "docker", "rm", "team/app", "--keep", "2", "--yes", "--concurrency", "1", "--json")
	mustExit(t, r, 6) // one of three deletions failed
	doc := decode[map[string]any](t, r, r.stdout)
	count := func(key string) int { return len(doc[key].([]any)) }
	if count("skipped") != 1 || count("missing") != 1 || count("failed") != 1 || count("deleted") != 0 {
		t.Fatalf("outcomes\n%s", r)
	}
	if s := doc["skipped"].([]any)[0].(map[string]any); s["tag"] != "v1" || !strings.Contains(s["reason"].(string), "sha256:again") {
		t.Fatalf("skipped %v", s)
	}
	if got := fake.ImageTags("docker-hosted", "team/app"); !slices.Equal(got, []string{"latest", "v1", "v3", "v4", "v5"}) {
		t.Fatalf("tags %q", got)
	}

	r = inv.run(t, "docker", "rm", "team/app", "--keep", "2", "--yes")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "2 tags deleted in")
}
