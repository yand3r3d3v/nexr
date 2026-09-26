package root_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yand3r3d3v/nexr/internal/nexus/nexustest"
)

func rawFixture(t *testing.T) (*nexustest.Server, invocation) {
	t.Helper()
	fake := nexustest.New(t)
	fake.AddRepo(nexustest.Repo{Name: "raw", Format: "raw", Type: "hosted", Online: true})
	fake.AddRepo(nexustest.Repo{Name: "once", Format: "raw", Type: "hosted", Online: true, WritePolicy: "ALLOW_ONCE"})
	return fake, admin(t, fake)
}

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

var sample = map[string]string{
	"bin/app":           "binary",
	"docs/read me.md":   "# readme",
	"docs/ю.txt":        "unicode",
	"notes.txt":         "notes",
	"docs/skip/tmp.log": "log",
}

func TestUpAndLs(t *testing.T) {
	fake, inv := rawFixture(t)
	src := tree(t, sample)

	r := inv.run(t, "up", src, "raw/app/1.0/", "--dry-run")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "would upload")
	mustContain(t, r, "stdout", "5 files, 29 B would be uploaded (dry run)")
	if len(fake.Paths("raw")) != 0 {
		t.Fatal("a dry run must not upload")
	}

	r = inv.run(t, "up", src, "raw/app/1.0/", "--exclude", "*.log", "--verify")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "uploaded  raw/app/1.0/docs/read me.md  (8 B)")
	mustContain(t, r, "stdout", "4 files, 26 B uploaded in")
	want := []string{"app/1.0/bin/app", "app/1.0/docs/read me.md", "app/1.0/docs/ю.txt", "app/1.0/notes.txt"}
	if got := fake.Paths("raw"); !slices.Equal(got, want) {
		t.Fatalf("stored %q", got)
	}

	r = inv.run(t, "ls", "raw/app/1.0/")
	mustExit(t, r, 0)
	if r.stdout != "bin/\ndocs/\nnotes.txt\n" {
		t.Fatalf("ls\n%s", r)
	}
	r = inv.run(t, "ls", "raw/app/1.0") // no trailing slash: a directory
	if r.stdout != "bin/\ndocs/\nnotes.txt\n" {
		t.Fatalf("ls without slash\n%s", r)
	}
	r = inv.run(t, "ls", "-l", "raw/app/1.0/docs")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "       8 B  ")
	mustContain(t, r, "stdout", "  read me.md\n")
	r = inv.run(t, "ls", "-r", "--sort", "name", "raw/app/")
	if r.stdout != "1.0/bin/app\n1.0/docs/read me.md\n1.0/docs/ю.txt\n1.0/notes.txt\n" {
		t.Fatalf("ls -r\n%s", r)
	}
	r = inv.run(t, "ls", "-r", "-q", "--match", "*.txt", "raw/app/")
	lines := strings.Fields(r.stdout)
	slices.Sort(lines)
	if !slices.Equal(lines, []string{"raw/app/1.0/docs/ю.txt", "raw/app/1.0/notes.txt"}) {
		t.Fatalf("ls -r -q --match\n%s", r)
	}
	r = inv.run(t, "ls", "raw/app/1.0/no") // a name prefix
	if r.stdout != "notes.txt\n" {
		t.Fatalf("prefix\n%s", r)
	}
	r = inv.run(t, "ls", "raw/app/1.0/notes.txt") // a file
	if r.stdout != "notes.txt\n" {
		t.Fatalf("file\n%s", r)
	}
	r = inv.run(t, "ls", "raw/app/2.0/")
	mustExit(t, r, 5)
	mustContain(t, r, "stderr", "nothing found at raw/app/2.0/")

	r = inv.run(t, "ls", "--json", "raw/app/1.0/")
	list := decode[[]map[string]any](t, r, r.stdout)
	if len(list) != 3 || list[0]["type"] != "directory" || list[2]["name"] != "notes.txt" || list[2]["size"] != float64(5) {
		t.Fatalf("ls --json\n%s", r)
	}
	file := list[2]
	checksum, _ := file["checksum"].(map[string]any)
	if file["asset_id"] == nil || checksum["sha256"] == nil || file["uploader"] != "admin" || file["last_downloaded"] != nil ||
		!strings.HasSuffix(file["download_url"].(string), "/repository/raw/app/1.0/notes.txt") {
		t.Fatalf("file entry %v", file)
	}
	r = inv.run(t, "ls", "--json", "-r", "raw/nothing/")
	mustExit(t, r, 5)
}

func TestUpJSONAndErrors(t *testing.T) {
	fake, inv := rawFixture(t)
	src := tree(t, map[string]string{"a.txt": "a", "b.txt": "b", "c.txt": "c"})

	r := inv.run(t, "up", filepath.Join(src, "a.txt"), "raw/x.txt", "--json")
	mustExit(t, r, 0)
	doc := decode[map[string]any](t, r, r.stdout)
	uploaded := doc["uploaded"].([]any)
	if doc["dry_run"] != false || len(uploaded) != 1 || uploaded[0].(map[string]any)["path"] != "x.txt" ||
		doc["summary"].(map[string]any)["bytes"] != float64(1) {
		t.Fatalf("up --json\n%s", r)
	}

	// Redeploy is disabled: one of three files fails, which is a partial failure.
	fake.PutFile("once", "b.txt", []byte("old"))
	r = inv.run(t, "up", filepath.Join(src, "a.txt"), filepath.Join(src, "b.txt"), filepath.Join(src, "c.txt"), "once/")
	mustExit(t, r, 6)
	mustContain(t, r, "stderr", "failed    once/b.txt")
	mustContain(t, r, "stderr", "nexr: 1 of 3 uploads failed")
	mustContain(t, r, "stdout", "2 files, 2 B uploaded in")
	mustContain(t, r, "stdout", ", 1 failed")
	r = inv.run(t, "up", filepath.Join(src, "b.txt"), "once/b.txt")
	mustExit(t, r, 9)
	mustContain(t, r, "stderr", "redeploy is not allowed")
	r = inv.run(t, "up", filepath.Join(src, "a.txt"), filepath.Join(src, "b.txt"), filepath.Join(src, "c.txt"), "once/", "--skip-existing")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "skipped   once/a.txt  (exists)")

	r = invocation{env: inv.env, stdin: "streamed"}.run(t, "up", "-", "raw/in/stdin.bin")
	mustExit(t, r, 0)
	if got, _ := fake.File("raw", "in/stdin.bin"); string(got) != "streamed" {
		t.Fatalf("stdin upload stored %q", got)
	}

	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"up", "raw/"}, "at least one source"},
		{[]string{"up", filepath.Join(src, "a.txt"), "raw/x", "--method", "scp"}, "invalid --method"},
		{[]string{"up", filepath.Join(src, "a.txt"), "raw/x", "--concurrency", "0"}, "invalid --concurrency"},
		{[]string{"up", src, filepath.Join(src, "a.txt"), "raw/x"}, "must be a directory"},
		{[]string{"up", filepath.Join(src, "a.txt"), "raw/a//x"}, "empty path segment"},
		{[]string{"ls", "raw/", "--sort", "color"}, "invalid --sort"},
		{[]string{"down"}, "needs REPO/PATH"},
		{[]string{"down", "raw/x", "a", "b"}, "needs REPO/PATH"},
		{[]string{"rm"}, "missing argument"},
		{[]string{"rm", "raw/x/", "--server-side"}, "needs -r"},
	} {
		r := inv.run(t, tt.args...)
		mustExit(t, r, 2)
		mustContain(t, r, "stderr", tt.want)
	}
}

func TestDown(t *testing.T) {
	fake, inv := rawFixture(t)
	for p, c := range sample {
		fake.PutFile("raw", "app/1.0/"+p, []byte(c))
	}
	dest := t.TempDir()
	r := inv.run(t, "down", "raw/app/1.0/", dest, "--exclude", "skip")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "4 files, 26 B downloaded in")
	for p, c := range sample {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(p)))
		if strings.HasPrefix(p, "docs/skip/") {
			if err == nil {
				t.Errorf("%s was excluded", p)
			}
			continue
		}
		if err != nil || string(got) != c {
			t.Errorf("%s: %q, %v", p, got, err)
		}
	}

	r = inv.run(t, "down", "raw/app/1.0/bin/app", "-")
	mustExit(t, r, 0)
	if r.stdout != "binary" || r.stderr != "" {
		t.Fatalf("stdout download\n%s", r)
	}
	r = inv.run(t, "down", "raw/app/1.0/notes.txt", dest+string(filepath.Separator), "--json")
	mustExit(t, r, 0)
	doc := decode[map[string]any](t, r, r.stdout)
	if d := doc["downloaded"].([]any); len(d) != 1 || d[0].(map[string]any)["destination"] != filepath.Join(dest, "notes.txt") {
		t.Fatalf("down --json\n%s", r)
	}
	r = inv.run(t, "down", "raw/app/1.0/notes.txt", dest+string(filepath.Separator), "--skip-existing")
	mustContain(t, r, "stdout", "skipped")

	r = inv.run(t, "down", "raw/app/9.9/", dest)
	mustExit(t, r, 5)
	r = inv.run(t, "down", "raw/app/1.0/", "-")
	mustExit(t, r, 2)
	r = inv.run(t, "down", "raw/app/1.0/notes.txt", "-", "--json")
	mustExit(t, r, 2)
}

func TestRm(t *testing.T) {
	fake, inv := rawFixture(t)
	put := func() {
		for p, c := range sample {
			fake.PutFile("raw", "app/"+p, []byte(c))
		}
		fake.PutFile("raw", "keep.txt", []byte("k"))
	}
	put()

	r := inv.run(t, "rm", "-r", "raw/app/", "--dry-run")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "would delete  raw/app/docs/read me.md")
	mustContain(t, r, "stdout", "5 files would be deleted (dry run)")

	r = inv.run(t, "rm", "-r", "raw/app/")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "refusing to delete 5 files without confirmation")
	r = inv.run(t, "rm", "raw/app")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "is a directory; deleting it needs -r")

	// On a terminal the user is asked.
	r = invocation{env: inv.env, stdin: "n\n", tty: true}.run(t, "rm", "-r", "raw/app/")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", "Delete? [y/N]")
	if len(fake.Paths("raw")) != 6 {
		t.Fatal("nothing may be deleted without confirmation")
	}
	r = invocation{env: inv.env, stdin: "y\n", tty: true}.run(t, "rm", "-r", "raw/app/", "--exclude", "*.log")
	mustExit(t, r, 0)
	mustContain(t, r, "stdout", "4 files deleted in")
	if got := fake.Paths("raw"); !slices.Equal(got, []string{"app/docs/skip/tmp.log", "keep.txt"}) {
		t.Fatalf("left %q", got)
	}

	r = inv.run(t, "rm", "raw/keep.txt")
	mustExit(t, r, 0)
	if r.stdout != "deleted  raw/keep.txt\n" {
		t.Fatalf("single delete\n%s", r)
	}
	r = inv.run(t, "rm", "raw/keep.txt")
	mustExit(t, r, 5)
	r = inv.run(t, "rm", "raw/keep.txt", "--ignore-missing", "--json")
	mustExit(t, r, 0)
	doc := decode[map[string]any](t, r, r.stdout)
	if len(doc["missing"].([]any)) != 1 || len(doc["deleted"].([]any)) != 0 {
		t.Fatalf("rm --ignore-missing --json\n%s", r)
	}
	r = inv.run(t, "rm", "raw/app/docs/skip/tmp.log", "raw/nope.txt")
	mustExit(t, r, 6)
	mustContain(t, r, "stderr", "failed    raw/nope.txt: not found")

	// The whole repository needs its name typed.
	put()
	r = invocation{env: inv.env, stdin: "yes\n", tty: true}.run(t, "rm", "-r", "raw")
	mustExit(t, r, 2)
	mustContain(t, r, "stderr", `Type "raw" to confirm`)
	r = invocation{env: inv.env, stdin: "raw\n", tty: true}.run(t, "rm", "-r", "raw")
	mustExit(t, r, 0)
	if len(fake.Paths("raw")) != 0 {
		t.Fatalf("left %q", fake.Paths("raw"))
	}
}

// AC-7: remote paths with "..", absolute paths or names reserved on the local
// system never cause writes outside DEST. Real Nexus normalises such paths,
// so they are planted in the fake.
func TestDownloadNeverLeavesDest(t *testing.T) {
	fake, inv := rawFixture(t)
	fake.PutFile("raw", "d/ok.txt", []byte("ok"))
	fake.PutFile("raw", "d/../../evil.txt", []byte("evil"))
	fake.PutFile("raw", "d/sub/../../../evil2.txt", []byte("evil"))
	if runtime.GOOS == "windows" {
		fake.PutFile("raw", "d/CON", []byte("reserved"))
		fake.PutFile("raw", "d/aux.txt", []byte("reserved"))
	}
	root := t.TempDir()
	dest := filepath.Join(root, "a", "b")
	r := inv.run(t, "down", "raw/d/", dest)
	mustExit(t, r, 6)
	mustContain(t, r, "stderr", "unsafe remote path")
	if got, err := os.ReadFile(filepath.Join(dest, "ok.txt")); err != nil || string(got) != "ok" {
		t.Fatalf("the safe file was not downloaded: %q, %v", got, err)
	}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && p != filepath.Join(dest, "ok.txt") {
			t.Errorf("unexpected file %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompletePaths(t *testing.T) {
	fake, inv := rawFixture(t)
	fake.PutFile("raw", "app/1.0/a.txt", []byte("a"))
	fake.PutFile("raw", "app/readme", []byte("r"))
	r := inv.run(t, "__complete", "ls", "ra")
	mustContain(t, r, "stdout", "raw/\n")
	mustContain(t, r, "stdout", ":6\n") // no space, no files
	r = inv.run(t, "__complete", "down", "raw/app/")
	mustContain(t, r, "stdout", "raw/app/1.0/\nraw/app/readme\n")
	r = inv.run(t, "__complete", "rm", "nope/x")
	mustExit(t, r, 0)
}
