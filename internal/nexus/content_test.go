package nexus_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/httpx"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/nexus/nexustest"
)

func rawFake(t *testing.T, opts ...nexustest.Option) (*nexustest.Server, *nexus.Client) {
	t.Helper()
	fake := nexustest.New(t, opts...)
	fake.AddRepo(nexustest.Repo{Name: "raw", Format: "raw", Type: "hosted", Online: true})
	return fake, client(t, fake, "admin", "admin123")
}

func body(s string) nexus.UploadBody {
	return nexus.UploadBody{
		Open:        func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(s)), nil },
		Size:        int64(len(s)),
		ContentType: "text/plain",
		Replayable:  true,
	}
}

// Names that need percent-encoding survive a round trip. They were all
// accepted by Nexus 3.96 (docs/nexus-api.md).
func TestContentRoundTrip(t *testing.T) {
	fake, c := rawFake(t)
	ctx := context.Background()
	names := []string{"plain.txt", "with space.txt", "semi;colon.txt", "hash#tag.txt", "q?mark.txt", "pct%41.txt",
		"plus+sign.txt", "brack[1].txt", "quote'1.txt", `dq"1.txt`, "юникод.txt", "comma,1.txt", "amp&1.txt",
		"colon:1.txt", " lead space", "trail space ", "..dots..", ".hidden"}
	for _, name := range names {
		path := "dir with space/" + name
		if err := c.Upload(ctx, "raw", path, body("content of "+name)); err != nil {
			t.Fatalf("upload %q: %v", name, err)
		}
		if got, ok := fake.File("raw", path); !ok || string(got) != "content of "+name {
			t.Fatalf("stored %q: %q, %v; paths %q", name, got, ok, fake.Paths("raw"))
		}
		info, err := c.Stat(ctx, "raw", path)
		sha := sha1.Sum([]byte("content of " + name))
		if err != nil || info.Size != int64(len("content of "+name)) || info.SHA1 != hex.EncodeToString(sha[:]) || info.LastModified.IsZero() {
			t.Fatalf("stat %q: %+v, %v", name, info, err)
		}
		rc, _, err := c.Download(ctx, "raw", path)
		if err != nil {
			t.Fatalf("download %q: %v", name, err)
		}
		got, _ := io.ReadAll(rc)
		_ = rc.Close()
		if string(got) != "content of "+name {
			t.Fatalf("downloaded %q: %q", name, got)
		}
	}
	if err := c.DeleteContent(ctx, "raw", "dir with space/semi;colon.txt"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteContent(ctx, "raw", "dir with space/semi;colon.txt"); errs.Classify(err) != errs.KindNotFound {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := c.Stat(ctx, "raw", "dir with space"); errs.Classify(err) != errs.KindNotFound {
		t.Fatalf("stat of a directory: %v", err)
	}
}

func TestUploadEdgeCases(t *testing.T) {
	fake, c := rawFake(t)
	ctx := context.Background()
	fake.AddRepo(nexustest.Repo{Name: "once", Format: "raw", Type: "hosted", WritePolicy: "ALLOW_ONCE"})
	fake.AddRepo(nexustest.Repo{Name: "ro", Format: "raw", Type: "hosted", WritePolicy: "DENY"})
	fake.AddRepo(nexustest.Repo{Name: "group", Format: "raw", Type: "group"})

	// A form content type would make Jetty swallow the body.
	b := body("form-looking")
	b.ContentType = "application/x-www-form-urlencoded"
	if err := c.Upload(ctx, "raw", "form.txt", b); err != nil {
		t.Fatal(err)
	}
	if got, _ := fake.File("raw", "form.txt"); string(got) != "form-looking" {
		t.Fatalf("stored %q", got)
	}
	empty := nexus.UploadBody{Open: func() (io.ReadCloser, error) { t.Error("an empty file must not be opened"); return nil, nil }, Size: 0}
	if err := c.Upload(ctx, "raw", "empty", empty); err != nil {
		t.Fatal(err)
	}
	if got, ok := fake.File("raw", "empty"); !ok || len(got) != 0 {
		t.Fatalf("empty file: %q %v", got, ok)
	}

	if err := c.Upload(ctx, "once", "x", body("1")); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		repo, want string
		kind       errs.Kind
	}{
		{"once", "cannot be updated as asset already exists", errs.KindRejected},
		{"ro", "is read-only", errs.KindRejected},
		{"group", "405", errs.KindRejected},
		{"nope", "Repository not found", errs.KindNotFound},
	} {
		err := c.Upload(ctx, tt.repo, "x", body("2"))
		if errs.Classify(err) != tt.kind || err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("upload to %s: %v", tt.repo, err)
		}
	}
}

func TestUploadRawComponent(t *testing.T) {
	fake, c := rawFake(t)
	if err := c.UploadRawComponent(context.Background(), "raw", "a b/c", "d;e.txt", body("via components")); err != nil {
		t.Fatal(err)
	}
	if got, _ := fake.File("raw", "a b/c/d;e.txt"); string(got) != "via components" {
		t.Fatalf("stored %q; paths %q", got, fake.Paths("raw"))
	}
	if err := c.UploadRawComponent(context.Background(), "raw", "", "root.txt", body("at the root")); err != nil {
		t.Fatal(err)
	}
	if got, _ := fake.File("raw", "root.txt"); string(got) != "at the root" {
		t.Fatalf("stored %q; paths %q", got, fake.Paths("raw"))
	}
}

func TestAssetsAndSearch(t *testing.T) {
	fake, c := rawFake(t)
	ctx := context.Background()
	for i := range 250 {
		fake.PutFile("raw", fmt.Sprintf("bulk/f%03d.txt", i), []byte("x"))
	}
	for _, p := range []string{"dir/a.txt", "dir/sub/b.txt", "dir-sibling/c.txt", "space dir/d.txt", `q"dir/e.txt`, "root.txt"} {
		fake.PutFile("raw", p, []byte(p))
	}
	n := 0
	for a, err := range c.Assets(ctx, "raw") {
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(a.Path, "/") || a.ID == "" || a.Size == 0 || a.Checksum.SHA256 == "" || a.LastModified.IsZero() {
			t.Fatalf("asset %+v", a)
		}
		n++
	}
	if n != 256 {
		t.Fatalf("scan returned %d assets", n)
	}

	search := func(group string) ([]string, error) {
		var paths []string
		for a, err := range c.SearchAssets(ctx, nexus.AssetQuery{Repository: "raw", Group: group}) {
			if err != nil {
				return nil, err
			}
			paths = append(paths, a.Path)
		}
		slices.Sort(paths)
		return paths, nil
	}
	for _, tt := range []struct {
		group string
		want  []string
	}{
		{nexus.QuotePath("dir"), []string{"dir/a.txt"}},
		{nexus.QuotePath(""), []string{"root.txt"}},
		{nexus.QuotePath("space dir"), []string{"space dir/d.txt"}},
		{nexus.QuotePath(`q"dir`), []string{`q"dir/e.txt`}},
		{"/dir*", []string{"dir-sibling/c.txt", "dir/a.txt", "dir/sub/b.txt"}},
		{"/space dir*", nil}, // false negative, as on Nexus
	} {
		got, err := search(tt.group)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("group %s: %q, %v", tt.group, got, err)
		}
	}
	if got, err := search("/bulk*"); err != nil || len(got) != 250 {
		t.Errorf("paginated search: %d results, %v", len(got), err)
	}
	if _, err := search("/d*"); errs.Classify(err) != errs.KindRejected || !strings.Contains(err.Error(), "3 characters") {
		t.Errorf("short wildcard: %v", err)
	}
}

func TestBrowse(t *testing.T) {
	fake, c := rawFake(t)
	ctx := context.Background()
	for _, p := range []string{"a/b.txt", "a/c/d.txt", "a/c", "a b/x"} {
		fake.PutFile("raw", p, []byte(p))
	}
	nodes, err := c.Browse(ctx, "raw", "a")
	if err != nil {
		t.Fatal(err)
	}
	want := []nexus.BrowseNode{{Name: "b.txt", File: true}, {Name: "c", File: true, Folder: true}}
	if !slices.Equal(nodes, want) {
		t.Fatalf("browse a = %+v", nodes)
	}
	if nodes, err := c.Browse(ctx, "raw", ""); err != nil || len(nodes) != 2 || nodes[0].Name != "a" || nodes[1].Name != "a b" || !nodes[1].Folder {
		t.Fatalf("browse root = %+v, %v", nodes, err)
	}
	if nodes, err := c.Browse(ctx, "raw", "missing"); err != nil || len(nodes) != 0 {
		t.Fatalf("browse missing = %+v, %v", nodes, err)
	}
	if _, err := c.Browse(ctx, "nope", ""); errs.Classify(err) != errs.KindNotFound {
		t.Fatalf("unknown repository: %v", err)
	}

	if err := c.DeleteFolder(ctx, "raw", "a"); err != nil {
		t.Fatal(err)
	}
	if got := fake.Paths("raw"); !slices.Equal(got, []string{"a b/x"}) {
		t.Fatalf("after folder delete: %q", got)
	}
	var id string
	for a, err := range c.Assets(ctx, "raw") {
		if err != nil {
			t.Fatal(err)
		}
		id = a.ID
	}
	if err := c.DeleteAsset(ctx, id); err != nil || len(fake.Paths("raw")) != 0 {
		t.Fatalf("delete asset: %v, %q", err, fake.Paths("raw"))
	}

	_, old := rawFake(t, nexustest.WithoutBrowseAPI())
	if _, err := old.Browse(ctx, "raw", ""); errs.Classify(err) != errs.KindNotFound {
		t.Fatalf("browse without the Browse API: %v", err)
	}
}

// A transfer that stops moving data fails with a timeout, however long the
// transfer as a whole may take.
func TestIdleTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 10))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c, _ := nexus.New(srv.URL, srv.Client(), time.Second)
	c.SetIdleTimeout(100 * time.Millisecond)
	rc, info, err := c.Download(context.Background(), "raw", "big")
	if err != nil || info.Size != 100 {
		t.Fatalf("download: %+v, %v", info, err)
	}
	defer rc.Close()
	start := time.Now()
	_, err = io.ReadAll(rc)
	if errs.Classify(err) != errs.KindTimeout || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("read: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the stalled transfer took %s to fail", d)
	}
}

// A replayable upload is sent again after a 503.
func TestUploadIsRetried(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	hc, _ := httpx.NewClient(httpx.Options{Retries: 2, Sleep: func(context.Context, time.Duration) error { return nil }})
	c, _ := nexus.New(srv.URL, hc, time.Second)
	if err := c.Upload(context.Background(), "raw", "f", body("payload")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(bodies, []string{"payload", "payload"}) {
		t.Fatalf("bodies = %q", bodies)
	}

	bodies = nil
	once := body("stream")
	once.Replayable = false
	if err := c.Upload(context.Background(), "raw", "f", once); errs.Classify(err) != errs.KindGeneric || len(bodies) != 1 {
		t.Fatalf("non-replayable upload: %v after %d requests", err, len(bodies))
	}
}

func TestContentURL(t *testing.T) {
	c, _ := nexus.New("https://nexus.example.com/nexus/", nil, 0)
	got := c.ContentURL("raw-hosted", "a b/c;d+e%f#g?h/ю.txt")
	want := "https://nexus.example.com/nexus/repository/raw-hosted/a%20b/c%3Bd+e%25f%23g%3Fh/%D1%8E.txt"
	if got != want {
		t.Fatalf("ContentURL = %s\nwant         %s", got, want)
	}
}
