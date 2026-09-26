package remote

import "testing"

func TestPattern(t *testing.T) {
	tests := []struct {
		pattern string
		match   []string
		noMatch []string
	}{
		{"raw-*", []string{"raw-hosted", "raw-"}, []string{"maven-raw", "raw/x"}},
		{"*.tar.gz", []string{"app.tar.gz"}, []string{"dir/app.tar.gz", "app.tar.gzip"}},
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
