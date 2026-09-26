package remote

import (
	"path/filepath"
	"testing"
)

func TestParsePath(t *testing.T) {
	tests := []struct {
		in   string
		want Path
	}{
		{"raw", Path{Repo: "raw", Dir: true}},
		{"raw/", Path{Repo: "raw", Dir: true}},
		{"raw/a", Path{Repo: "raw", Path: "a"}},
		{"raw/a/b/", Path{Repo: "raw", Path: "a/b", Dir: true}},
		{"raw//a/b", Path{Repo: "raw", Path: "a/b"}},
		{"raw-1.x_y/dir with space/ю.txt", Path{Repo: "raw-1.x_y", Path: "dir with space/ю.txt"}},
	}
	for _, tt := range tests {
		got, err := ParsePath(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParsePath(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "/raw", "_raw/x", "ra w/x", "raw/a//b", "raw/a/../b", "raw/./a", "raw/a//", `raw/a\b`, "raw/.."} {
		if _, err := ParsePath(bad); err == nil {
			t.Errorf("ParsePath(%q) succeeded", bad)
		}
	}
}

func TestPathHelpers(t *testing.T) {
	p, _ := ParsePath("raw/a/b.txt")
	if p.String() != "raw/a/b.txt" || p.Base() != "b.txt" || p.Parent() != "a" || p.Ref("x") != "raw/x" {
		t.Fatalf("helpers of %+v", p)
	}
	root, _ := ParsePath("raw")
	if root.String() != "raw/" || root.Base() != "" || root.Parent() != "" {
		t.Fatalf("helpers of the root %+v", root)
	}
	dir, _ := ParsePath("raw/a/")
	if dir.String() != "raw/a/" || dir.Parent() != "" {
		t.Fatalf("helpers of %+v", dir)
	}
	if Join("", "x") != "x" || Join("a", "") != "a" || Join("a", "x") != "a/x" {
		t.Fatal("Join")
	}
	for _, tt := range []struct {
		dir, path, want string
		ok              bool
	}{
		{"a", "a/b", "b", true}, {"a", "ab/c", "", false}, {"a", "a", "", false}, {"", "x/y", "x/y", true},
	} {
		if got, ok := Rel(tt.dir, tt.path); got != tt.want || ok != tt.ok {
			t.Errorf("Rel(%q, %q) = %q, %v", tt.dir, tt.path, got, ok)
		}
	}
}

func TestLocalPath(t *testing.T) {
	dest := t.TempDir()
	for _, rel := range []string{"a.txt", "dir/sub/b.txt", "with space/ю.txt", "..dots..", ".hidden", "a:b"} {
		got, err := localPath(dest, rel, "linux")
		if err != nil || got != filepath.Join(dest, filepath.FromSlash(rel)) {
			t.Errorf("localPath(%q) = %q, %v", rel, got, err)
		}
	}
	for _, rel := range []string{"", "/etc/passwd", "../x", "a/../../x", "a//b", "./a", "a/\x00b", "a/\nb"} {
		if got, err := localPath(dest, rel, "linux"); err == nil {
			t.Errorf("localPath(%q) = %q, want an error", rel, got)
		}
	}
	for _, rel := range []string{"C:/x", "a:b", "CON", "con.txt", "dir/aux", "COM1.log", "LPT9", "trail.", "trail ", `a"b`, "a|b", "a?b", "a*b", "a<b"} {
		if _, err := localPath(dest, rel, "windows"); err == nil {
			t.Errorf("localPath(%q) on Windows succeeded", rel)
		}
	}
	for _, rel := range []string{"console.txt", "com10", "auxiliary", "a b.txt"} {
		if _, err := localPath(dest, rel, "windows"); err != nil {
			t.Errorf("localPath(%q) on Windows: %v", rel, err)
		}
	}
}
