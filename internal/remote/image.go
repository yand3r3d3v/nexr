package remote

import (
	"fmt"
	"regexp"
	"strings"
)

// ImageRef is an image reference, [HOST/]NAME[:TAG], as used with docker pull
// (spec FR-IMGREF-1, FR-IMGREF-3).
type ImageRef struct {
	Host string // registry host[:port]; empty when not given
	Name string // e.g. "team/app"
	Tag  string // empty when not given
}

var (
	// nameComponent is a path component of an image name in the OCI
	// distribution grammar.
	nameComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	tagRE         = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
)

// ParseImageRef parses an image reference. A first component that contains
// "." or ":", or is "localhost", is a registry host, as for docker.
func ParseImageRef(s string) (ImageRef, error) {
	var r ImageRef
	rest := s
	if strings.Contains(rest, "@") {
		return r, fmt.Errorf("invalid image reference %q: references by digest are not supported; use a tag", s)
	}
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		rest, r.Tag = rest[:i], rest[i+1:]
		if !tagRE.MatchString(r.Tag) {
			return r, fmt.Errorf("invalid tag %q in %q: use letters, digits, '_', '.' and '-', at most 128 characters", r.Tag, s)
		}
	}
	if first, after, ok := strings.Cut(rest, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Host, rest = first, after
	}
	if rest == "" {
		return r, fmt.Errorf("invalid image reference %q: no image name", s)
	}
	for _, c := range strings.Split(rest, "/") {
		if !nameComponent.MatchString(c) {
			if strings.ToLower(c) != c {
				return r, fmt.Errorf("invalid image name %q: image names are lower-case", rest)
			}
			return r, fmt.Errorf("invalid image name %q: each component uses a-z, 0-9 and the separators '.', '_', '__' and '-'", rest)
		}
	}
	r.Name = rest
	return r, nil
}

// String returns the reference as given, without a missing host or tag.
func (r ImageRef) String() string {
	s := r.Name
	if r.Host != "" {
		s = r.Host + "/" + s
	}
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	return s
}

// IsPattern reports whether an argument is a pattern (FR-DRM-7) rather than
// an image reference: a regular expression or a glob.
func IsPattern(s string) bool {
	return strings.HasPrefix(s, "re:") || strings.ContainsAny(s, "*?[")
}
