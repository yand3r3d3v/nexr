package remote

import "testing"

func TestPattern(t *testing.T) {
	tests := []struct {
		pattern string
		match   []string
		noMatch []string
	}{
		{"raw-*", []string{"raw-hosted", "raw-"}, []string{"maven-raw", "raw/x"}},
		{"*.tar.gz", []string{"app.tar.gz", "dir/app.tar.gz"}, []string{"app.tar.gzip", "app.tar.gz/x"}},
		{"/*.tar.gz", []string{"app.tar.gz"}, []string{"dir/app.tar.gz"}},
		{"node_modules", []string{"node_modules", "a/b/node_modules"}, []string{"node_modules/x", "my_node_modules"}},
		{"sub/*.log", []string{"sub/a.log"}, []string{"x/sub/a.log", "sub/x/a.log"}},
		{"**/*.log", []string{"a.log", "x/y/z.log"}, []string{"a.txt"}},
		{"build/**", []string{"build/a", "build/x/y"}, []string{"builds/a"}},
		{"v?", []string{"v1"}, []string{"v10", "v"}},
		{"[!a]*", []string{"bcd"}, []string{"abc"}},
		{"release-[0-9]*", []string{"release-1.2"}, []string{"release-x"}},
		{"re:^v\\d+\\.\\d+\\.\\d+$", []string{"v1.2.3"}, []string{"v1.2", "latest"}},
		{"a\\*b", []string{"a*b"}, []string{"axb"}},
	}
	for _, tt := range tests {
		p, err := ParsePattern(tt.pattern)
		if err != nil {
			t.Fatalf("ParsePattern(%q): %v", tt.pattern, err)
		}
		for _, s := range tt.match {
			if !p.Match(s) {
				t.Errorf("%q should match %q", tt.pattern, s)
			}
		}
		for _, s := range tt.noMatch {
			if p.Match(s) {
				t.Errorf("%q should not match %q", tt.pattern, s)
			}
		}
	}
	for _, bad := range []string{"re:(", "[abc"} {
		if _, err := ParsePattern(bad); err == nil {
			t.Errorf("ParsePattern(%q) succeeded", bad)
		}
	}
	ps, err := ParsePatterns([]string{"a*", "re:^b"})
	if err != nil || !MatchAny(ps, "bx") || MatchAny(ps, "cx") || ps[0].String() != "a*" {
		t.Fatalf("ParsePatterns: %v %v", ps, err)
	}
}

func TestSelected(t *testing.T) {
	inc, _ := ParsePatterns([]string{"docs", "*.md"})
	exc, _ := ParsePatterns([]string{"skip", "/top.md"})
	for rel, want := range map[string]bool{
		"docs/a.txt":      true,  // below an included directory
		"x/readme.md":     true,  // included by name at any depth
		"top.md":          false, // excluded at the top level only
		"x/top.md":        true,
		"docs/skip/b.txt": false, // below an excluded directory
		"src/main.go":     false, // not included
	} {
		if got := Selected(inc, exc, rel); got != want {
			t.Errorf("Selected(%q) = %v", rel, got)
		}
	}
	if !Selected(nil, nil, "anything") {
		t.Error("no patterns select everything")
	}
}
