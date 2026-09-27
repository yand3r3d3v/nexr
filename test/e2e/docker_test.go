//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

const dockerRepo = "docker-e2e"

// Media types pushed by docker (single-platform images) and by crane or
// buildx (OCI images and indexes).
const (
	dockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	ociManifest    = "application/vnd.oci.image.manifest.v1+json"
	ociIndex       = "application/vnd.oci.image.index.v1+json"
)

// pusher pushes images through the Registry API of the e2e repository with
// the calls of docker push: blob uploads, then the manifest.
type pusher struct {
	t    *testing.T
	base string // <NEXUS_URL>/repository/docker-e2e
}

func newPusher(t *testing.T) pusher {
	return pusher{t: t, base: strings.TrimRight(os.Getenv("NEXUS_URL"), "/") + "/repository/" + dockerRepo}
}

func (p pusher) do(method, target, contentType string, body []byte) *http.Response {
	p.t.Helper()
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		p.t.Fatal(err)
	}
	req.SetBasicAuth(os.Getenv("NEXUS_USER"), os.Getenv("NEXUS_PASSWORD"))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", strings.Join([]string{ociIndex, ociManifest, dockerManifest}, ", "))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	return resp
}

func digestOf(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }

// blob uploads data in one request and returns its digest.
func (p pusher) blob(name string, data []byte) string {
	p.t.Helper()
	resp := p.do(http.MethodPost, p.base+"/v2/"+name+"/blobs/uploads/", "", nil)
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusAccepted || loc == "" {
		p.t.Fatalf("starting an upload: %s", resp.Status)
	}
	// Behind /repository/REPO/, Nexus returns the location without that prefix.
	if strings.HasPrefix(loc, "/v2/") {
		loc = p.base + loc
	}
	u, err := url.Parse(loc)
	if err != nil {
		p.t.Fatal(err)
	}
	digest := digestOf(data)
	q := u.Query()
	q.Set("digest", digest)
	u.RawQuery = q.Encode()
	resp = p.do(http.MethodPut, u.String(), "application/octet-stream", data)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		p.t.Fatalf("uploading a blob: %s", resp.Status)
	}
	return digest
}

// manifest stores a manifest under ref (a tag) and returns its digest.
func (p pusher) manifest(name, ref, mediaType string, body []byte) string {
	p.t.Helper()
	resp := p.do(http.MethodPut, p.base+"/v2/"+name+"/manifests/"+ref, mediaType, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		p.t.Fatalf("pushing %s:%s: %s", name, ref, resp.Status)
	}
	if got := resp.Header.Get("Docker-Content-Digest"); got != digestOf(body) {
		p.t.Fatalf("pushing %s:%s: digest %s, want %s", name, ref, got, digestOf(body))
	}
	return digestOf(body)
}

type descriptor struct {
	MediaType string            `json:"mediaType"`
	Size      int               `json:"size"`
	Digest    string            `json:"digest"`
	Platform  map[string]string `json:"platform,omitempty"`
}

// image pushes a single-platform image with one layer whose content makes it
// unique, and returns the descriptor of its manifest.
func (p pusher) image(name, tag, mediaType, os, arch, content string) descriptor {
	p.t.Helper()
	layer := []byte(content)
	config, _ := json.Marshal(map[string]any{
		"architecture": arch, "os": os, "created": time.Now().UTC().Format(time.RFC3339),
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{digestOf(layer)}}, "config": map[string]any{},
	})
	configType, layerType := "application/vnd.oci.image.config.v1+json", "application/vnd.oci.image.layer.v1.tar"
	if mediaType == dockerManifest {
		configType, layerType = "application/vnd.docker.container.image.v1+json", "application/vnd.docker.image.rootfs.diff.tar"
	}
	body, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": mediaType,
		"config": descriptor{MediaType: configType, Size: len(config), Digest: p.blob(name, config)},
		"layers": []descriptor{{MediaType: layerType, Size: len(layer), Digest: p.blob(name, layer)}},
	})
	return descriptor{MediaType: mediaType, Size: len(body), Digest: p.manifest(name, tag, mediaType, body),
		Platform: map[string]string{"os": os, "architecture": arch}}
}

// index pushes a multi-platform index of manifests.
func (p pusher) index(name, tag string, manifests ...descriptor) string {
	p.t.Helper()
	body, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": ociIndex, "manifests": manifests})
	return p.manifest(name, tag, ociIndex, body)
}

// retag points tag at the manifest ref points to, like "crane tag".
func (p pusher) retag(name, ref, tag string) string {
	p.t.Helper()
	resp := p.do(http.MethodGet, p.base+"/v2/"+name+"/manifests/"+ref, "", nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.t.Fatalf("reading %s:%s: %s", name, ref, resp.Status)
	}
	return p.manifest(name, tag, resp.Header.Get("Content-Type"), body)
}

// pull fetches a manifest by tag or digest and, for an index, each child
// manifest, then every blob they reference, as docker pull would for all
// platforms. It returns the digest of the manifest.
func (p pusher) pull(name, ref string) string {
	p.t.Helper()
	resp := p.do(http.MethodGet, p.base+"/v2/"+name+"/manifests/"+ref, "", nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.t.Fatalf("pulling %s@%s: %s", name, ref, resp.Status)
	}
	var m struct {
		Manifests []descriptor `json:"manifests"`
		Config    descriptor   `json:"config"`
		Layers    []descriptor `json:"layers"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		p.t.Fatal(err)
	}
	for _, child := range m.Manifests {
		p.pull(name, child.Digest)
	}
	for _, b := range append(m.Layers, m.Config) {
		if b.Digest == "" {
			continue
		}
		resp := p.do(http.MethodGet, p.base+"/v2/"+name+"/blobs/"+b.Digest, "", nil)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			p.t.Fatalf("pulling blob %s of %s: %s", b.Digest, name, resp.Status)
		}
	}
	return digestOf(body)
}

// imagePrefix returns an image name prefix that is new for every test run.
func imagePrefix(t *testing.T) string {
	return fmt.Sprintf("e2e/%s-%d", strings.ToLower(t.Name()), time.Now().UnixNano())
}

type tagInfo struct {
	Tag         string  `json:"tag"`
	Digest      string  `json:"digest"`
	MediaType   string  `json:"media_type"`
	Pushed      *string `json:"pushed"`
	Size        *int64  `json:"size"`
	ComponentID *string `json:"component_id"`
}

// waitTags waits until "docker tags" lists want, all of them indexed: the
// search index lags a few seconds behind pushes.
func waitTags(t *testing.T, image string, want ...string) map[string]tagInfo {
	t.Helper()
	slices.Sort(want)
	deadline := time.Now().Add(60 * time.Second)
	for {
		r := nexr(t, []string{"NEXR_DOCKER_REPO=" + dockerRepo}, "docker", "tags", image, "--json")
		var list []tagInfo
		if r.code == 0 {
			if err := json.Unmarshal([]byte(r.stdout), &list); err != nil {
				t.Fatal(err)
			}
		}
		byTag := map[string]tagInfo{}
		var names []string
		indexed := true
		for _, ti := range list {
			byTag[ti.Tag] = ti
			names = append(names, ti.Tag)
			indexed = indexed && ti.Pushed != nil
		}
		slices.Sort(names)
		if slices.Equal(names, want) && indexed {
			return byTag
		}
		if time.Now().After(deadline) {
			t.Fatalf("tags of %s: %q (indexed: %v), want %q\n%s", image, names, indexed, want, r.stderr)
		}
		time.Sleep(time.Second)
	}
}

func docker(t *testing.T, args ...string) result {
	t.Helper()
	return nexr(t, []string{"NEXR_DOCKER_REPO=" + dockerRepo}, append([]string{"docker"}, args...)...)
}

// AC-8: digests and push times of images pushed like docker push does, and
// of a multi-platform index pushed like crane does.
func TestDockerTags(t *testing.T) {
	p := newPusher(t)
	pfx := imagePrefix(t)
	app, multi := pfx+"/app", pfx+"/multi"
	start := time.Now().Add(-time.Minute) // tolerate clock skew with the server

	d10 := p.image(app, "1.0", dockerManifest, "linux", "amd64", "one")
	time.Sleep(1100 * time.Millisecond)
	d11 := p.image(app, "1.1", dockerManifest, "linux", "amd64", "two")
	time.Sleep(1100 * time.Millisecond)
	p.retag(app, "1.1", "latest")
	amd := p.image(multi, "amd64", ociManifest, "linux", "amd64", "amd")
	arm := p.image(multi, "arm64", ociManifest, "linux", "arm64", "arm")
	dIndex := p.index(multi, "1.0", amd, arm)

	tags := waitTags(t, app, "1.0", "1.1", "latest")
	if tags["1.0"].Digest != d10.Digest || tags["1.1"].Digest != d11.Digest || tags["latest"].Digest != d11.Digest {
		t.Fatalf("digests %+v", tags)
	}
	pushed := func(tag string) time.Time {
		ts, err := time.Parse(time.RFC3339, *tags[tag].Pushed)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	if !pushed("1.0").Before(pushed("1.1")) || pushed("latest").Before(pushed("1.1")) ||
		pushed("1.0").Before(start.Truncate(time.Second)) || pushed("latest").After(time.Now().Add(time.Minute)) {
		t.Fatalf("push times 1.0 %s, 1.1 %s, latest %s", pushed("1.0"), pushed("1.1"), pushed("latest"))
	}
	if tags["1.0"].MediaType != dockerManifest {
		t.Fatalf("media type %s", tags["1.0"].MediaType)
	}
	r := docker(t, "tags", app, "-q")
	check(t, r, 0)
	if r.stdout != "latest\n1.1\n1.0\n" {
		t.Fatalf("newest push first:\n%s", r.stdout)
	}

	mt := waitTags(t, multi, "1.0", "amd64", "arm64")
	if ix := mt["1.0"]; ix.Digest != dIndex || ix.MediaType != ociIndex || ix.Size != nil {
		t.Fatalf("index %+v", ix)
	}
	if mt["arm64"].Digest != arm.Digest {
		t.Fatalf("child %+v", mt["arm64"])
	}

	r = docker(t, "ls", "--json", "--match", pfx+"/*")
	check(t, r, 0)
	var images []struct{ Name string }
	if err := json.Unmarshal([]byte(r.stdout), &images); err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || images[0].Name != app || images[1].Name != multi {
		t.Fatalf("docker ls: %s", r.stdout)
	}
}

// AC-9: --keep N keeps the N newest unprotected tags, latest is protected,
// deleting a tag leaves the tags of the same manifest intact, and a
// multi-platform image stays pullable after deletions of other tags and the
// Docker cleanup task.
func TestDockerRetention(t *testing.T) {
	p := newPusher(t)
	pfx := imagePrefix(t)
	ret, multi := pfx+"/ret", pfx+"/multi"
	for i := 1; i <= 5; i++ {
		p.image(ret, fmt.Sprintf("v%d", i), dockerManifest, "linux", "amd64", fmt.Sprintf("v%d", i))
	}
	v5 := p.retag(ret, "v5", "latest")
	amd := p.image(multi, "amd64", ociManifest, "linux", "amd64", "amd")
	arm := p.image(multi, "arm64", ociManifest, "linux", "arm64", "arm")
	dIndex := p.index(multi, "1.0", amd, arm)
	waitTags(t, ret, "latest", "v1", "v2", "v3", "v4", "v5")
	waitTags(t, multi, "1.0", "amd64", "arm64")

	r := docker(t, "rm", ret, "--keep", "2", "--dry-run", "--json")
	check(t, r, 0)
	var plan struct {
		Images []struct {
			Decisions []struct{ Tag, Action, Reason string }
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &plan); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range plan.Images[0].Decisions {
		got = append(got, d.Tag+":"+d.Action)
	}
	want := []string{"latest:keep", "v5:keep", "v4:keep", "v3:delete", "v2:delete", "v1:delete"}
	if !slices.Equal(got, want) {
		t.Fatalf("plan %q, want %q", got, want)
	}

	check(t, docker(t, "rm", ret, "--keep", "2"), 2) // no terminal, no --yes
	r = docker(t, "rm", ret, "--keep", "2", "--yes")
	check(t, r, 0)
	if !strings.Contains(r.stdout, "3 tags deleted") {
		t.Fatalf("rm --keep 2:\n%s", r.stdout)
	}
	r = docker(t, "tags", ret, "-q", "--sort", "name")
	check(t, r, 0)
	if r.stdout != "latest\nv4\nv5\n" {
		t.Fatalf("tags after rm --keep 2:\n%s", r.stdout)
	}

	// latest points to the manifest of v5 and survives its deletion.
	check(t, docker(t, "rm", ret+":v5"), 0)
	if got := p.pull(ret, "latest"); got != v5 {
		t.Fatalf("latest is %s, want %s", got, v5)
	}

	// Deleting the tags of the platform manifests keeps the index whole.
	check(t, docker(t, "rm", multi+":amd64", multi+":arm64", "--yes"), 0)
	if runDockerCleanup(t) {
		t.Log("ran the Docker cleanup task")
	}
	if got := p.pull(multi, "1.0"); got != dIndex {
		t.Fatalf("index digest %s, want %s", got, dIndex)
	}
	r = docker(t, "tags", multi, "-q")
	check(t, r, 0)
	if r.stdout != "1.0\n" {
		t.Fatalf("tags of the multi-platform image:\n%s", r.stdout)
	}
}

// runDockerCleanup runs "Docker - Delete unused manifests and images" for the
// e2e repository with no grace period, creating the task where the server
// allows it (3.96, not 3.71). It reports whether the task ran.
func runDockerCleanup(t *testing.T) bool {
	t.Helper()
	api := strings.TrimRight(os.Getenv("NEXUS_URL"), "/") + "/service/rest/v1/tasks"
	call := func(method, target string, body any) (*http.Response, []byte) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, target, rd)
		req.SetBasicAuth(os.Getenv("NEXUS_USER"), os.Getenv("NEXUS_PASSWORD"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp, data
	}
	resp, data := call(http.MethodPost, api, map[string]any{
		"type": "repository.docker.gc", "name": fmt.Sprintf("nexr-e2e-docker-gc-%d", time.Now().UnixNano()),
		"enabled": true, "notificationCondition": "FAILURE", "frequency": map[string]any{"schedule": "manual"},
		"properties": map[string]any{"repositoryName": dockerRepo, "deployOffset": "0"},
	})
	if resp.StatusCode == http.StatusMethodNotAllowed {
		t.Log("this server cannot create tasks through the API; skipping the Docker cleanup task")
		return false
	}
	var task struct{ ID string }
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(data, &task) != nil {
		t.Fatalf("creating the Docker cleanup task: %s %s", resp.Status, data)
	}
	defer call(http.MethodDelete, api+"/"+task.ID, nil)
	if resp, data := call(http.MethodPost, api+"/"+task.ID+"/run", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("running the Docker cleanup task: %s %s", resp.Status, data)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		_, data := call(http.MethodGet, api+"/"+task.ID, nil)
		var st struct {
			CurrentState  string  `json:"currentState"`
			LastRunResult *string `json:"lastRunResult"`
		}
		if err := json.Unmarshal(data, &st); err != nil {
			t.Fatal(err)
		}
		if st.CurrentState == "WAITING" && st.LastRunResult != nil {
			if *st.LastRunResult != "OK" {
				t.Fatalf("the Docker cleanup task ended with %s", *st.LastRunResult)
			}
			return true
		}
	}
	t.Fatal("the Docker cleanup task did not finish in time")
	return false
}

// FR-NET-3: a reverse proxy that serves the registry at the root of a host,
// like https://registry.example.com/v2/ → /repository/REPO/v2/, and a Docker
// connector port.
func TestDockerRegistryURL(t *testing.T) {
	p := newPusher(t)
	pfx := imagePrefix(t)
	app := pfx + "/app"
	d := p.image(app, "1.0", ociManifest, "linux", "amd64", "proxy")
	waitTags(t, app, "1.0")

	target, err := url.Parse(os.Getenv("NEXUS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v2/") {
			http.NotFound(w, r)
			return
		}
		r.URL.Path = strings.TrimRight(target.Path, "/") + "/repository/" + dockerRepo + r.URL.Path
		r.URL.RawPath = ""
		rp.ServeHTTP(w, r)
	}))
	defer proxy.Close()

	r := docker(t, "tags", app+":1.0", "--registry-url", proxy.URL, "--json")
	check(t, r, 0)
	if !strings.Contains(r.stdout, d.Digest) {
		t.Fatalf("tags through the proxy:\n%s", r.stdout)
	}
	r = nexr(t, []string{"NEXR_DOCKER_REPO=" + dockerRepo, "NEXR_DOCKER_REGISTRY_URL=" + proxy.URL}, "docker", "ls", "--match", pfx+"/*")
	check(t, r, 0)
	if r.stdout != app+"\n" {
		t.Fatalf("catalog through the proxy:\n%s", r.stdout)
	}
	// An image reference with the registry host selects the repository.
	host := strings.TrimPrefix(proxy.URL, "http://")
	r = nexr(t, []string{"NEXR_DOCKER_REGISTRY_URL=" + proxy.URL, "NEXR_DOCKER_REPO=" + dockerRepo}, "docker", "tags", host+"/"+app, "-q")
	check(t, r, 0)
	if r.stdout != "1.0\n" {
		t.Fatalf("reference with the registry host:\n%s", r.stdout)
	}

	if reg := os.Getenv("NEXR_E2E_REGISTRY"); reg != "" {
		r = docker(t, "tags", app, "--registry-url", "http://"+reg, "-q")
		check(t, r, 0)
		if r.stdout != "1.0\n" {
			t.Fatalf("tags through the Docker connector:\n%s", r.stdout)
		}
	}
	check(t, docker(t, "rm", app+":1.0"), 0)
}
