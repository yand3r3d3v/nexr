// Package remote parses the addresses and patterns used on the command line.
package remote

import (
	"fmt"
	"regexp"
	"strings"
)

// Pattern matches names or paths. It is a glob (*, ?, [...] and ** for any
// number of path segments) or, with the prefix "re:", an RE2 regular
// expression (docs/specification.md, FR-PAT-1). As in .gitignore, a glob
// without "/" matches the last segment of a path at any depth, a glob with
// "/" matches the whole path, and a leading "/" anchors a glob to the base
// directory.
type Pattern struct {
	raw  string
	re   *regexp.Regexp
	base bool // match the last path segment only
}

// ParsePattern compiles a pattern.
func ParsePattern(s string) (Pattern, error) {
	if expr, ok := strings.CutPrefix(s, "re:"); ok {
		re, err := regexp.Compile(expr)
		if err != nil {
			return Pattern{}, fmt.Errorf("invalid regular expression %q: %w", expr, err)
		}
		return Pattern{raw: s, re: re}, nil
	}
	glob := strings.TrimPrefix(s, "/")
	re, err := globToRegexp(glob)
	if err != nil {
		return Pattern{}, err
	}
	return Pattern{raw: s, re: re, base: !strings.Contains(s, "/")}, nil
}

// ParsePatterns compiles several patterns.
func ParsePatterns(list []string) ([]Pattern, error) {
	out := make([]Pattern, 0, len(list))
	for _, s := range list {
		p, err := ParsePattern(s)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Match reports whether s, a name or a "/"-separated path, matches.
func (p Pattern) Match(s string) bool {
	if p.base {
		s = s[strings.LastIndex(s, "/")+1:]
	}
	return p.re.MatchString(s)
}

// String returns the pattern as given.
func (p Pattern) String() string { return p.raw }

// MatchTree reports whether a pattern matches the relative path rel or one of
// its parent directories, so that "--exclude build" also excludes the files
// below every directory named build.
func MatchTree(patterns []Pattern, rel string) bool {
	for {
		if MatchAny(patterns, rel) {
			return true
		}
		i := strings.LastIndex(rel, "/")
		if i < 0 {
			return false
		}
		rel = rel[:i]
	}
}

// Selected applies --include and --exclude to a relative path: it is kept
// when it matches an include pattern (or there are none) and no exclude
// pattern, each also through its parent directories.
func Selected(include, exclude []Pattern, rel string) bool {
	return (len(include) == 0 || MatchTree(include, rel)) && !MatchTree(exclude, rel)
}

// MatchAny reports whether s matches any of the patterns.
func MatchAny(patterns []Pattern, s string) bool {
	for _, p := range patterns {
		if p.Match(s) {
			return true
		}
	}
	return false
}

func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				// "**/" also matches zero directories.
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				return nil, fmt.Errorf("invalid pattern %q: unterminated [", glob)
			}
			class := glob[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case '\\':
			if i+1 < len(glob) {
				i++
				b.WriteString(regexp.QuoteMeta(string(glob[i])))
			} else {
				b.WriteString(`\\`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %w", glob, err)
	}
	return re, nil
}
