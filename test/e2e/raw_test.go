//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const rawRepo = "raw-e2e"

// prefix returns a remote directory that is new for every test run.
func prefix(t *testing.T) string {
	return fmt.Sprintf("%s/%s-%d", rawRepo, strings.ToLower(t.Name()), time.Now().UnixNano())
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// waitListed waits until "ls -r" shows n files below dir: the search index of
// Nexus lags a few seconds behind uploads.
func waitListed(t *testing.T, dir string, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		r := nexr(t, nil, "ls", "-r", "--json", dir+"/")
		var list []map[string]any
		if r.code == 0 {
			if err := json.Unmarshal([]byte(r.stdout), &list); err != nil {
				t.Fatal(err)
			}
			if len(list) == n {
				return list
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d files below %s, got %d (exit %d)\n%s", n, dir, len(list), r.code, r.stderr)
		}
		time.Sleep(time.Second)
	}
}

// AC-4: up → down round trips keep content and layout, also for names with
// spaces and non-ASCII characters.
func TestRawRoundTrip(t *testing.T) {
	src := t.TempDir()
	files := map[string]string{
		"bin/app":                  "binary",
		"docs/read me.md":          "# readme",
		"docs/ünïcödé/файл.txt":    "unicode",
		"docs/semi;colon#hash.txt": "special",
		".hidden":                  "hidden",
		"empty":                    "",
	}
	writeFiles(t, src, files)
	dir := prefix(t)
	r := nexr(t, nil, "up", src, dir+"/", "--verify")
	check(t, r, 0)
	list := waitListed(t, dir, len(files))
	for _, e := range list {
		if e["checksum"].(map[string]any)["sha256"] == nil {
			t.Fatalf("no checksum: %v", e)
		}
	}

	dest := t.TempDir()
	r = nexr(t, nil, "down", dir+"/", dest)
	check(t, r, 0)
	for p, want := range files {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(p)))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v", p, got, err)
		}
	}
	r = nexr(t, nil, "down", dir+"/docs/read me.md", "-")
	check(t, r, 0)
	if r.stdout != "# readme" {
		t.Fatalf("stdout download: %q", r.stdout)
	}
	check(t, nexr(t, nil, "rm", "-r", "--yes", dir+"/"), 0)
}

// AC-4: uploading and downloading a large file keeps the resident memory of
// nexr below 64 MiB. NEXR_E2E_LARGE_MB sets the size (default 1024).
func TestLargeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("large file")
	}
	size := int64(1024)
	if v := os.Getenv("NEXR_E2E_LARGE_MB"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		size = n
	}
	size <<= 20
	src := filepath.Join(t.TempDir(), "large.bin")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	rng := rand.NewChaCha8([32]byte{1})
	if _, err := io.CopyN(io.MultiWriter(f, h), rng, size); err != nil {
		t.Fatal(err)
	}
	f.Close()
	want := hex.EncodeToString(h.Sum(nil))

	dir := prefix(t)
	r, rss := nexrRSS(t, "up", src, dir+"/")
	check(t, r, 0)
	t.Logf("upload of %d MiB: peak RSS %d MiB", size>>20, rss>>20)
	if runtime.GOOS == "linux" && rss > 64<<20 {
		t.Errorf("upload used %d MiB of memory", rss>>20)
	}
	waitListed(t, dir, 1)
	dest := t.TempDir()
	r, rss = nexrRSS(t, "down", dir+"/large.bin", dest)
	check(t, r, 0)
	t.Logf("download of %d MiB: peak RSS %d MiB", size>>20, rss>>20)
	if runtime.GOOS == "linux" && rss > 64<<20 {
		t.Errorf("download used %d MiB of memory", rss>>20)
	}
	g, err := os.Open(filepath.Join(dest, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	h2 := sha256.New()
	if _, err := io.Copy(h2, g); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(h2.Sum(nil)); got != want {
		t.Fatalf("SHA-256 %s, want %s", got, want)
	}
	check(t, nexr(t, nil, "rm", dir+"/large.bin"), 0)
}

// nexrRSS runs nexr and returns its peak resident memory in bytes (Linux).
func nexrRSS(t *testing.T, args ...string) (result, int64) {
	t.Helper()
	cmd := command(t, nil, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{stdout: stdout.String(), stderr: stderr.String()}
	if cmd.ProcessState != nil {
		r.code = cmd.ProcessState.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	var rss int64
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		rss = int64(ru.Maxrss) * 1024 // kilobytes on Linux
	}
	return r, rss
}

// faultProxy forwards to Nexus and injects failures into uploads: the first
// PUT of a path containing "flaky" gets a 503 and the second a 500, then the
// upload passes; every PUT of a path containing "broken" gets a 500.
func faultProxy(t *testing.T) (proxyURL string, flakyAttempts func() int) {
	t.Helper()
	target, err := url.Parse(os.Getenv("NEXUS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	var mu sync.Mutex
	attempts := map[string]int{}
	total := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			switch {
			case strings.Contains(r.URL.Path, "broken"):
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusInternalServerError)
				return
			case strings.Contains(r.URL.Path, "flaky"):
				mu.Lock()
				attempts[r.URL.Path]++
				n := attempts[r.URL.Path]
				total++
				mu.Unlock()
				if n <= 2 {
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader([]int{http.StatusServiceUnavailable, http.StatusInternalServerError}[n-1])
					return
				}
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() int { mu.Lock(); defer mu.Unlock(); return total }
}

// AC-5: 1,000 files upload with --concurrency 8; injected 500/503 failures
// are retried, and permanent failures end with exit code 6.
func TestManyFilesWithFaults(t *testing.T) {
	src := t.TempDir()
	files := map[string]string{}
	for i := range 1000 {
		name := fmt.Sprintf("d%02d/f%04d.txt", i%20, i)
		switch i {
		case 7, 500:
			name = fmt.Sprintf("d%02d/flaky%04d.txt", i%20, i)
		case 13, 999:
			name = fmt.Sprintf("d%02d/broken%04d.txt", i%20, i)
		}
		files[name] = strconv.Itoa(i)
	}
	writeFiles(t, src, files)
	proxyURL, flakyAttempts := faultProxy(t)
	dir := prefix(t)

	start := time.Now()
	r := nexr(t, []string{"NEXUS_URL=" + proxyURL}, "up", src, dir+"/", "--concurrency", "8", "--json")
	t.Logf("uploaded 1000 files in %s", time.Since(start).Round(time.Millisecond))
	check(t, r, 6)
	var doc struct {
		Uploaded []struct{ Path string } `json:"uploaded"`
		Failed   []struct {
			Path  string
			Error struct{ Code string }
		} `json:"failed"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	var failed []string
	for _, f := range doc.Failed {
		failed = append(failed, f.Path[strings.LastIndex(f.Path, "/")+1:])
	}
	slices.Sort(failed)
	if len(doc.Uploaded) != 998 || !slices.Equal(failed, []string{"broken0013.txt", "broken0999.txt"}) {
		t.Fatalf("uploaded %d, failed %q", len(doc.Uploaded), failed)
	}
	if n := flakyAttempts(); n != 6 {
		t.Fatalf("%d attempts for the two flaky files, want 3 each", n)
	}
	if !strings.Contains(r.stderr, "2 of 1000 uploads failed") {
		t.Fatalf("stderr: %s", r.stderr)
	}
	waitListed(t, dir, 998)
	check(t, nexr(t, nil, "rm", "-r", "--yes", dir+"/"), 0)
}

// AC-6: "rm -r --dry-run" lists exactly what "rm -r" deletes, and a bulk
// deletion without --yes in a non-interactive shell deletes nothing.
func TestRemovePlanMatchesDeletion(t *testing.T) {
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"a.txt": "a", "sub/b.txt": "b", "sub/c.log": "c", "sub/deeper/d.txt": "d"})
	dir := prefix(t)
	check(t, nexr(t, nil, "up", src, dir+"/"), 0)
	waitListed(t, dir, 4)

	paths := func(r result, key string) []string {
		t.Helper()
		var doc map[string]json.RawMessage
		var items []struct{ Path string }
		if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
			t.Fatalf("%v\n%s", err, r.stdout)
		}
		if err := json.Unmarshal(doc[key], &items); err != nil {
			t.Fatalf("%v\n%s", err, r.stdout)
		}
		var out []string
		for _, e := range items {
			out = append(out, e.Path)
		}
		slices.Sort(out)
		return out
	}
	dry := nexr(t, nil, "rm", "-r", dir+"/", "--exclude", "*.log", "--dry-run", "--json")
	check(t, dry, 0)
	planned := paths(dry, "deleted")
	if len(planned) != 3 {
		t.Fatalf("dry run planned %q", planned)
	}

	r := nexr(t, nil, "rm", "-r", dir+"/", "--exclude", "*.log")
	check(t, r, 2)
	waitListed(t, dir, 4)

	r = nexr(t, nil, "rm", "-r", dir+"/", "--exclude", "*.log", "--yes", "--json")
	check(t, r, 0)
	if deleted := paths(r, "deleted"); !slices.Equal(deleted, planned) {
		t.Fatalf("deleted %q, planned %q", deleted, planned)
	}
	waitListed(t, dir, 1)
	check(t, nexr(t, nil, "rm", "-r", "--yes", dir+"/"), 0)
}
