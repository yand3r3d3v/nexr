package files_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/files"
	"github.com/yand3r3d3v/nexr/internal/httpx"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/nexus/nexustest"
	"github.com/yand3r3d3v/nexr/internal/remote"
)

type env struct {
	fake *nexustest.Server
	api  *nexus.Client
	svc  *files.Service
	ctx  context.Context
}

func setup(t *testing.T, opts ...nexustest.Option) *env {
	t.Helper()
	fake := nexustest.New(t, opts...)
	fake.AddRepo(nexustest.Repo{Name: "raw", Format: "raw", Type: "hosted", Online: true})
	fake.AddRepo(nexustest.Repo{Name: "maven", Format: "maven2", Type: "hosted", Online: true})
	fake.AddRepo(nexustest.Repo{Name: "group", Format: "raw", Type: "group", Online: true,
		Settings: map[string]any{"group": map[string]any{"memberNames": []any{"raw", "proxy"}}}})
	fake.AddRepo(nexustest.Repo{Name: "proxy", Format: "raw", Type: "proxy", Online: true})
	hc, err := httpx.NewClient(httpx.Options{
		Username: "admin", Password: func() (string, error) { return "admin123", nil }, AuthURLs: []string{fake.BaseURL()},
	})
	if err != nil {
		t.Fatal(err)
	}
	api, err := nexus.New(fake.BaseURL(), hc, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return &env{fake: fake, api: api, svc: files.New(api), ctx: context.Background()}
}

func (e *env) repo(t *testing.T, name string) nexus.Repository {
	t.Helper()
	r, err := e.svc.Repository(e.ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *env) put(paths ...string) {
	for _, p := range paths {
		e.fake.PutFile("raw", p, []byte("content of "+p))
	}
}

func (e *env) requests(prefix string) int {
	n := 0
	for _, r := range e.fake.Requests() {
		if strings.Contains(r, prefix) {
			n++
		}
	}
	return n
}

func names(entries []files.Entry) []string {
	var out []string
	for _, e := range entries {
		n := e.Name
		if e.Dir {
			n += "/"
		}
		out = append(out, n)
	}
	return out
}

func paths(t *testing.T, seq func(func(files.Entry, error) bool)) []string {
	t.Helper()
	var out []string
	for e, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e.Path)
	}
	slices.Sort(out)
	return out
}

func mustPath(t *testing.T, s string) remote.Path {
	t.Helper()
	p, err := remote.ParsePath(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestListDir(t *testing.T) {
	e := setup(t)
	e.put("a/one.txt", "a/sub/two.txt", "a/both", "a/both/three.txt", "root.txt", "z/x")
	raw := e.repo(t, "raw")

	entries, exists, err := e.svc.ListDir(e.ctx, raw, "a", true)
	if err != nil || !exists {
		t.Fatal(exists, err)
	}
	if got := names(entries); !slices.Equal(got, []string{"both/", "sub/", "both", "one.txt"}) {
		t.Fatalf("entries = %q", got)
	}
	one := entries[3]
	if one.Size != int64(len("content of a/one.txt")) || one.Checksum.SHA256 == "" || one.AssetID == "" || one.LastModified.IsZero() || one.Uploader != "admin" {
		t.Fatalf("metadata = %+v", one)
	}
	root, exists, err := e.svc.ListDir(e.ctx, raw, "", false)
	if err != nil || !exists || !slices.Equal(names(root), []string{"a/", "z/", "root.txt"}) {
		t.Fatalf("root = %q, %v, %v", names(root), exists, err)
	}
	if _, exists, err := e.svc.ListDir(e.ctx, raw, "missing", false); err != nil || exists {
		t.Fatalf("missing dir: %v %v", exists, err)
	}

	// Files that the search index does not know get their metadata from HEAD.
	e.fake.PutFile("maven", "com/x/app-1.0.jar", []byte("jar"))
	entries, _, err = e.svc.ListDir(e.ctx, e.repo(t, "maven"), "com/x", true)
	if err != nil || len(entries) != 1 || entries[0].Size != 3 || entries[0].Checksum.SHA1 == "" {
		t.Fatalf("maven entries = %+v, %v", entries, err)
	}
}

func TestListDirWithoutBrowseAPI(t *testing.T) {
	e := setup(t, nexustest.WithoutBrowseAPI())
	e.put("a/one.txt", "a/sub/two.txt", "a/sub/deeper/four.txt", "abc/x")
	entries, exists, err := e.svc.ListDir(e.ctx, e.repo(t, "raw"), "a", true)
	if err != nil || !exists || !slices.Equal(names(entries), []string{"sub/", "one.txt"}) {
		t.Fatalf("entries = %q, %v, %v", names(entries), exists, err)
	}
	if entries[1].Checksum.SHA256 == "" {
		t.Fatalf("metadata = %+v", entries[1])
	}
}

func TestWalkStrategies(t *testing.T) {
	e := setup(t)
	e.put("dir/a.txt", "dir/sub/b.txt", "dir-sibling/c.txt", "space dir/d.txt", "space dir/sub/e.txt", "root.txt")
	raw := e.repo(t, "raw")

	before := e.requests("/v1/search/assets")
	if got := paths(t, e.svc.Walk(e.ctx, raw, "dir")); !slices.Equal(got, []string{"dir/a.txt", "dir/sub/b.txt"}) {
		t.Fatalf("walk dir = %q", got)
	}
	if e.requests("/v1/search/assets") != before+1 || e.requests("/v1/assets") != 0 {
		t.Fatalf("a prefix search was expected: %q", e.fake.Requests())
	}
	// Names with spaces break wildcard searches: traverse the browse tree.
	if got := paths(t, e.svc.Walk(e.ctx, raw, "space dir")); !slices.Equal(got, []string{"space dir/d.txt", "space dir/sub/e.txt"}) {
		t.Fatalf("walk space dir = %q", got)
	}
	// Short prefixes and the root are scanned.
	e.put("ab/f.txt")
	if got := paths(t, e.svc.Walk(e.ctx, raw, "ab")); !slices.Equal(got, []string{"ab/f.txt"}) {
		t.Fatalf("walk ab = %q", got)
	}
	if got := paths(t, e.svc.Walk(e.ctx, raw, "")); len(got) != 7 {
		t.Fatalf("walk root = %q", got)
	}
	if e.requests("GET /service/rest/v1/assets") == 0 {
		t.Fatal("a scan was expected")
	}
	e.fake.PutFile("maven", "com/x/app.jar", []byte("x"))
	if got := paths(t, e.svc.Walk(e.ctx, e.repo(t, "maven"), "com")); !slices.Equal(got, []string{"com/x/app.jar"}) {
		t.Fatalf("walk maven = %q", got)
	}
}

func TestStatFileAndDirExists(t *testing.T) {
	e := setup(t)
	e.put("a/b.txt", "a/b.txt/c")
	raw := e.repo(t, "raw")
	f, ok, err := e.svc.StatFile(e.ctx, raw, "a/b.txt")
	if err != nil || !ok || f.AssetID == "" || f.Checksum.SHA256 == "" {
		t.Fatalf("stat = %+v, %v, %v", f, ok, err)
	}
	if _, ok, err := e.svc.StatFile(e.ctx, raw, "a"); ok || err != nil {
		t.Fatalf("a directory is not a file: %v %v", ok, err)
	}
	for dir, want := range map[string]bool{"a": true, "a/b.txt": true, "x": false, "": true} {
		if got, err := e.svc.DirExists(e.ctx, raw, dir); err != nil || got != want {
			t.Errorf("DirExists(%q) = %v, %v", dir, got, err)
		}
	}
	if _, err := e.svc.Repository(e.ctx, "nope"); errs.Classify(err) != errs.KindNotFound || !strings.Contains(err.Error(), `repository "nope" not found`) {
		t.Fatalf("unknown repository: %v", err)
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
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

func planned(p *files.UploadPlan) []string {
	var out []string
	for _, it := range p.Items {
		out = append(out, it.Path)
	}
	slices.Sort(out)
	return out
}

func TestPlanUpload(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"build/bin/x": "x", "build/readme.md": "r", "build/.hidden": "h", "build/node_modules/m.js": "m",
		"a.txt": "a", "other/a.txt": "a2",
	})
	build, a := filepath.Join(dir, "build"), filepath.Join(dir, "a.txt")
	plan := func(dest string, opts files.UploadOptions, sources ...string) ([]string, error) {
		p, err := e.svc.PlanUpload(e.ctx, sources, mustPath(t, dest), opts)
		if err != nil {
			return nil, err
		}
		return planned(p), nil
	}
	tests := []struct {
		dest    string
		sources []string
		opts    files.UploadOptions
		want    []string
	}{
		{"raw", []string{a}, files.UploadOptions{}, []string{"a.txt"}},
		{"raw/dir/", []string{a}, files.UploadOptions{}, []string{"dir/a.txt"}},
		{"raw/dir/b.txt", []string{a}, files.UploadOptions{}, []string{"dir/b.txt"}},
		{"raw/app/1.0", []string{build}, files.UploadOptions{}, []string{"app/1.0/.hidden", "app/1.0/bin/x", "app/1.0/node_modules/m.js", "app/1.0/readme.md"}},
		{"raw/app/1.0/", []string{build}, files.UploadOptions{}, []string{"app/1.0/.hidden", "app/1.0/bin/x", "app/1.0/node_modules/m.js", "app/1.0/readme.md"}},
		{"raw", []string{build}, files.UploadOptions{}, []string{".hidden", "bin/x", "node_modules/m.js", "readme.md"}},
		{"raw/x/", []string{build, a}, files.UploadOptions{}, []string{"x/.hidden", "x/a.txt", "x/bin/x", "x/node_modules/m.js", "x/readme.md"}},
		{"raw/x/", []string{build}, files.UploadOptions{Exclude: pats(t, "node_modules", ".*")}, []string{"x/bin/x", "x/readme.md"}},
		{"raw/x/", []string{build}, files.UploadOptions{Include: pats(t, "**/*.md", "bin/**")}, []string{"x/bin/x", "x/readme.md"}},
	}
	for _, tt := range tests {
		got, err := plan(tt.dest, tt.opts, tt.sources...)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("up %v %s: %q, %v; want %q", tt.sources, tt.dest, got, err, tt.want)
		}
	}

	failures := []struct {
		dest    string
		sources []string
		opts    files.UploadOptions
		kind    errs.Kind
		want    string
	}{
		{"raw/x", []string{a, build}, files.UploadOptions{}, errs.KindUsage, "must be a directory"},
		{"raw/x/", []string{a, filepath.Join(dir, "other", "a.txt")}, files.UploadOptions{}, errs.KindUsage, "same remote file"},
		{"raw/x/", []string{filepath.Join(dir, "missing")}, files.UploadOptions{}, errs.KindNotFound, "cannot read"},
		{"raw/x/", []string{build}, files.UploadOptions{ContentType: "text/plain"}, errs.KindUsage, "--content-type"},
		{"maven/x/", []string{a}, files.UploadOptions{}, errs.KindUsage, "raw repositories only"},
		{"group/x/", []string{a}, files.UploadOptions{}, errs.KindUsage, "hosted members of group: raw"},
		{"proxy/x/", []string{a}, files.UploadOptions{}, errs.KindUsage, "proxy repository"},
		{"nope/x/", []string{a}, files.UploadOptions{}, errs.KindNotFound, "not found"},
		{"raw/x/", []string{"-", a}, files.UploadOptions{}, errs.KindUsage, "stdin"},
		{"raw/x/", []string{"-"}, files.UploadOptions{}, errs.KindUsage, "full remote path"},
	}
	for _, tt := range failures {
		_, err := plan(tt.dest, tt.opts, tt.sources...)
		msg := ""
		if err != nil {
			msg = err.Error() + " " + strings.Join(errs.HintsOf(err), " ")
		}
		if errs.Classify(err) != tt.kind || !strings.Contains(msg, tt.want) {
			t.Errorf("up %v %s: %v; want %v containing %q", tt.sources, tt.dest, err, tt.kind, tt.want)
		}
	}
	if got, err := plan("raw/in.bin", files.UploadOptions{}, "-"); err != nil || !slices.Equal(got, []string{"in.bin"}) {
		t.Errorf("stdin: %q, %v", got, err)
	}
}

func pats(t *testing.T, list ...string) []remote.Pattern {
	t.Helper()
	p, err := remote.ParsePatterns(list)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanUploadSymlinksAndBackslashes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links and backslashes in names are Unix cases")
	}
	e := setup(t)
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"src/f.txt": "f", "target/t.txt": "t"})
	src := filepath.Join(dir, "src")
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, filepath.Join(src, "loop")); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.PlanUpload(e.ctx, []string{src}, mustPath(t, "raw/x/"), files.UploadOptions{})
	if err != nil || !slices.Equal(planned(p), []string{"x/f.txt"}) || len(p.Warnings) != 2 {
		t.Fatalf("without --follow-symlinks: %q, %q, %v", planned(p), p.Warnings, err)
	}
	p, err = e.svc.PlanUpload(e.ctx, []string{src}, mustPath(t, "raw/x/"), files.UploadOptions{FollowSymlinks: true})
	if err != nil || !slices.Equal(planned(p), []string{"x/f.txt", "x/link/t.txt"}) || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "loop") {
		t.Fatalf("with --follow-symlinks: %q, %q, %v", planned(p), p.Warnings, err)
	}

	writeTree(t, dir, map[string]string{`bs/a\b.txt`: "x"})
	_, err = e.svc.PlanUpload(e.ctx, []string{filepath.Join(dir, "bs")}, mustPath(t, "raw/x/"), files.UploadOptions{})
	if errs.Classify(err) != errs.KindUsage || !strings.Contains(err.Error(), "directory separator") {
		t.Fatalf("backslash: %v", err)
	}
}

func TestUpload(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"build/a.txt": "alpha", "build/sub/b.bin": "beta", "build/empty": ""})
	plan, err := e.svc.PlanUpload(e.ctx, []string{filepath.Join(dir, "build")}, mustPath(t, "raw/v1/"), files.UploadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"put", "components"} {
		for _, it := range plan.Items {
			n, err := e.svc.Upload(e.ctx, plan, it, files.UploadOptions{Method: method, Verify: true}, nil)
			if err != nil || n != it.Size {
				t.Fatalf("%s %s: %d, %v", method, it.Path, n, err)
			}
		}
	}
	if got, _ := e.fake.File("raw", "v1/sub/b.bin"); string(got) != "beta" {
		t.Fatalf("stored %q", got)
	}
	if got, ok := e.fake.File("raw", "v1/empty"); !ok || len(got) != 0 {
		t.Fatalf("empty file: %q %v", got, ok)
	}
	_, err = e.svc.Upload(e.ctx, plan, plan.Items[0], files.UploadOptions{SkipExisting: true}, nil)
	if !files.IsSkip(err) {
		t.Fatalf("skip existing: %v", err)
	}

	stdinPlan, _ := e.svc.PlanUpload(e.ctx, []string{"-"}, mustPath(t, "raw/in/stdin.txt"), files.UploadOptions{})
	n, err := e.svc.Upload(e.ctx, stdinPlan, stdinPlan.Items[0], files.UploadOptions{Verify: true}, strings.NewReader("from stdin"))
	if err != nil || n != 10 {
		t.Fatalf("stdin: %d, %v", n, err)
	}
	if got, _ := e.fake.File("raw", "in/stdin.txt"); string(got) != "from stdin" {
		t.Fatalf("stored %q", got)
	}

	e.fake.AddRepo(nexustest.Repo{Name: "once", Format: "raw", Type: "hosted", WritePolicy: "ALLOW_ONCE"})
	oncePlan, _ := e.svc.PlanUpload(e.ctx, []string{filepath.Join(dir, "build", "a.txt")}, mustPath(t, "once/a.txt"), files.UploadOptions{})
	if _, err := e.svc.Upload(e.ctx, oncePlan, oncePlan.Items[0], files.UploadOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Upload(e.ctx, oncePlan, oncePlan.Items[0], files.UploadOptions{}, nil); errs.Classify(err) != errs.KindRejected {
		t.Fatalf("redeploy: %v", err)
	}
}

func TestPlanDownload(t *testing.T) {
	e := setup(t)
	e.put("app/1.0/a.txt", "app/1.0/sub/b.txt", "app/1.0/sub/c.log", "app/10/x", "single.txt")
	dest := t.TempDir()
	existing := filepath.Join(dest, "existing")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	local := func(src, to string, opts files.DownloadOptions) ([]string, *files.DownloadPlan) {
		t.Helper()
		p, err := e.svc.PlanDownload(e.ctx, mustPath(t, src), to, opts)
		if err != nil {
			t.Fatalf("plan %s → %s: %v", src, to, err)
		}
		var out []string
		for _, it := range p.Items {
			out = append(out, it.Local)
		}
		return out, p
	}
	if got, _ := local("raw/single.txt", "", files.DownloadOptions{}); !slices.Equal(got, []string{"single.txt"}) {
		t.Errorf("file to cwd: %q", got)
	}
	if got, _ := local("raw/single.txt", existing, files.DownloadOptions{}); !slices.Equal(got, []string{filepath.Join(existing, "single.txt")}) {
		t.Errorf("file to dir: %q", got)
	}
	if got, _ := local("raw/single.txt", filepath.Join(dest, "new.txt"), files.DownloadOptions{}); !slices.Equal(got, []string{filepath.Join(dest, "new.txt")}) {
		t.Errorf("file to name: %q", got)
	}
	if _, p := local("raw/single.txt", "-", files.DownloadOptions{}); !p.ToStdout {
		t.Errorf("file to stdout")
	}
	want := []string{filepath.Join(existing, "a.txt"), filepath.Join(existing, "sub", "b.txt"), filepath.Join(existing, "sub", "c.log")}
	for _, src := range []string{"raw/app/1.0", "raw/app/1.0/"} {
		if got, _ := local(src, existing, files.DownloadOptions{}); !slices.Equal(got, want) {
			t.Errorf("dir %s: %q", src, got)
		}
	}
	if got, _ := local("raw/app/1.0/", existing, files.DownloadOptions{Exclude: pats(t, "*.log")}); len(got) != 2 {
		t.Errorf("exclude: %q", got)
	}
	if _, err := e.svc.PlanDownload(e.ctx, mustPath(t, "raw/app/1.0/"), "-", files.DownloadOptions{}); errs.Classify(err) != errs.KindUsage {
		t.Errorf("dir to stdout: %v", err)
	}
	if _, err := e.svc.PlanDownload(e.ctx, mustPath(t, "raw/missing"), "", files.DownloadOptions{}); errs.Classify(err) != errs.KindNotFound {
		t.Errorf("missing: %v", err)
	}
}

func TestDownload(t *testing.T) {
	e := setup(t)
	e.put("d/a.txt", "d/sub/b.txt")
	dest := t.TempDir()
	plan, err := e.svc.PlanDownload(e.ctx, mustPath(t, "raw/d/"), dest, files.DownloadOptions{})
	if err != nil || len(plan.Items) != 2 {
		t.Fatalf("plan: %+v, %v", plan, err)
	}
	for _, it := range plan.Items {
		if _, err := e.svc.Download(e.ctx, plan, it, files.DownloadOptions{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dest, "sub", "b.txt"))
	if err != nil || string(got) != "content of d/sub/b.txt" {
		t.Fatalf("downloaded %q, %v", got, err)
	}
	fi, _ := os.Stat(filepath.Join(dest, "a.txt"))
	if fi == nil || !fi.ModTime().Equal(plan.Items[0].LastModified) {
		t.Fatalf("mtime %v, want %v", fi.ModTime(), plan.Items[0].LastModified)
	}
	if _, err := e.svc.Download(e.ctx, plan, plan.Items[0], files.DownloadOptions{SkipExisting: true}, nil); !files.IsSkip(err) {
		t.Fatalf("skip existing: %v", err)
	}

	// A checksum mismatch discards the file and leaves no temporary file.
	bad := plan.Items[0]
	bad.Local = filepath.Join(dest, "bad.txt")
	bad.SHA256 = strings.Repeat("0", 64)
	if _, err := e.svc.Download(e.ctx, plan, bad, files.DownloadOptions{}, nil); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("mismatch: %v", err)
	}
	entries, _ := os.ReadDir(dest)
	for _, de := range entries {
		if de.Name() == "bad.txt" || strings.HasPrefix(de.Name(), ".nexr-") {
			t.Fatalf("left behind: %s", de.Name())
		}
	}
	if _, err := e.svc.Download(e.ctx, plan, bad, files.DownloadOptions{NoVerify: true}, nil); err != nil {
		t.Fatalf("--no-verify: %v", err)
	}

	var out bytes.Buffer
	single, _ := e.svc.PlanDownload(e.ctx, mustPath(t, "raw/d/a.txt"), "-", files.DownloadOptions{})
	if n, err := e.svc.Download(e.ctx, single, single.Items[0], files.DownloadOptions{}, &out); err != nil || n != int64(out.Len()) || out.String() != "content of d/a.txt" {
		t.Fatalf("stdout: %q, %v", out.String(), err)
	}

	unsafe := plan.Items[0]
	unsafe.Err = errors.New(`unsafe remote path "../x"`)
	if _, err := e.svc.Download(e.ctx, plan, unsafe, files.DownloadOptions{}, nil); errs.Classify(err) != errs.KindRejected {
		t.Fatalf("unsafe: %v", err)
	}
}

func TestPlanAndRemove(t *testing.T) {
	e := setup(t)
	e.put("f.txt", "d/a.txt", "d/sub/b.txt", "d/sub/c.log", "both", "both/x")
	plan := func(opts files.RemoveOptions, targets ...string) (*files.RemovePlan, error) {
		var ts []remote.Path
		for _, s := range targets {
			ts = append(ts, mustPath(t, s))
		}
		return e.svc.PlanRemove(e.ctx, ts, opts)
	}
	refs := func(p *files.RemovePlan) []string {
		var out []string
		for _, it := range p.Items {
			r := it.Ref()
			if it.Missing {
				r += " (missing)"
			}
			out = append(out, r)
		}
		slices.Sort(out)
		return out
	}
	tests := []struct {
		targets []string
		opts    files.RemoveOptions
		want    []string
	}{
		{[]string{"raw/f.txt"}, files.RemoveOptions{}, []string{"raw/f.txt"}},
		{[]string{"raw/d/"}, files.RemoveOptions{Recursive: true}, []string{"raw/d/a.txt", "raw/d/sub/b.txt", "raw/d/sub/c.log"}},
		{[]string{"raw/d"}, files.RemoveOptions{Recursive: true, Exclude: pats(t, "**/*.log")}, []string{"raw/d/a.txt", "raw/d/sub/b.txt"}},
		{[]string{"raw/both"}, files.RemoveOptions{}, []string{"raw/both"}},
		{[]string{"raw/both"}, files.RemoveOptions{Recursive: true}, []string{"raw/both", "raw/both/x"}},
		{[]string{"raw/nope", "raw/f.txt"}, files.RemoveOptions{}, []string{"raw/f.txt", "raw/nope (missing)"}},
		{[]string{"raw/d/"}, files.RemoveOptions{Recursive: true, ServerSide: true}, []string{"raw/d/"}},
	}
	for _, tt := range tests {
		p, err := plan(tt.opts, tt.targets...)
		if err != nil || !slices.Equal(refs(p), tt.want) {
			t.Errorf("rm %v: %q, %v; want %q", tt.targets, refs(p), err, tt.want)
		}
	}
	for _, target := range []string{"raw/d", "raw/d/", "raw"} {
		if _, err := plan(files.RemoveOptions{}, target); errs.Classify(err) != errs.KindUsage || !strings.Contains(err.Error(), "-r") {
			t.Errorf("rm %s without -r: %v", target, err)
		}
	}
	whole, err := plan(files.RemoveOptions{Recursive: true}, "raw")
	if err != nil || len(whole.WholeRepos) != 1 || whole.Files() != 6 {
		t.Fatalf("whole repository: %+v, %v", whole, err)
	}

	p, _ := plan(files.RemoveOptions{Recursive: true}, "raw/d/")
	for _, it := range p.Items {
		if err := e.svc.Remove(e.ctx, it, files.RemoveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.Remove(e.ctx, p.Items[0], files.RemoveOptions{}); !files.IsMissing(err) {
		t.Fatalf("second delete: %v", err)
	}
	if got := e.fake.Paths("raw"); !slices.Equal(got, []string{"both", "both/x", "f.txt"}) {
		t.Fatalf("left: %q", got)
	}
	missing, _ := plan(files.RemoveOptions{}, "raw/nope")
	if err := e.svc.Remove(e.ctx, missing.Items[0], files.RemoveOptions{}); errs.Classify(err) != errs.KindNotFound {
		t.Fatalf("missing: %v", err)
	}

	folder, _ := plan(files.RemoveOptions{Recursive: true, ServerSide: true}, "raw/both/")
	if err := e.svc.Remove(e.ctx, folder.Items[0], files.RemoveOptions{WaitTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if got := e.fake.Paths("raw"); !slices.Equal(got, []string{"both", "f.txt"}) {
		t.Fatalf("after server-side delete: %q", got)
	}

	// Other formats are deleted by asset ID.
	e.fake.PutFile("maven", "com/x/app.jar", []byte("x"))
	mp, err := plan(files.RemoveOptions{}, "maven/com/x/app.jar")
	if err != nil || len(mp.Items) != 1 {
		t.Fatal(mp, err)
	}
	if err := e.svc.Remove(e.ctx, mp.Items[0], files.RemoveOptions{}); err != nil || len(e.fake.Paths("maven")) != 0 {
		t.Fatalf("maven delete: %v, %q", err, e.fake.Paths("maven"))
	}
	for _, r := range e.fake.Requests() {
		if strings.HasPrefix(r, "DELETE /repository/maven") {
			t.Fatalf("maven files must be deleted by asset ID: %s", r)
		}
	}
}
