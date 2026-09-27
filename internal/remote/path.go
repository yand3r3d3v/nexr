package remote

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Path is a remote address REPO[/PATH] (docs/specification.md §4.2).
type Path struct {
	Repo string
	// Path has no leading or trailing slash; "" is the repository root.
	Path string
	// Dir is set when the address ends with "/" or names the root.
	Dir bool
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9-][A-Za-z0-9_.-]*$`)

// ParsePath parses REPO[/PATH]. A leading slash of PATH is ignored; empty,
// "." and ".." segments and backslashes are rejected.
func ParsePath(s string) (Path, error) {
	repo, rest, _ := strings.Cut(s, "/")
	if !repoRe.MatchString(repo) {
		return Path{}, fmt.Errorf("invalid repository name %q in %q", repo, s)
	}
	rest = strings.TrimLeft(rest, "/")
	p := Path{Repo: repo, Dir: rest == "" || strings.HasSuffix(rest, "/")}
	p.Path = strings.TrimSuffix(rest, "/")
	if strings.Contains(p.Path, `\`) {
		return Path{}, fmt.Errorf("invalid path %q: remote paths use \"/\" as separator, and Nexus turns \"\\\" into \"/\"", s)
	}
	if p.Path != "" {
		for _, seg := range strings.Split(p.Path, "/") {
			switch seg {
			case "":
				return Path{}, fmt.Errorf("invalid path %q: empty path segment", s)
			case ".", "..":
				return Path{}, fmt.Errorf("invalid path %q: %q segments are not allowed", s, seg)
			}
		}
	}
	return p, nil
}

// String returns REPO/PATH, with a trailing slash for directories.
func (p Path) String() string {
	switch {
	case p.Path == "":
		return p.Repo + "/"
	case p.Dir:
		return p.Repo + "/" + p.Path + "/"
	}
	return p.Repo + "/" + p.Path
}

// Ref returns the display form REPO/PATH of a path in the same repository.
func (p Path) Ref(path string) string {
	return p.Repo + "/" + path
}

// Base returns the last segment of the path, or "" for the root.
func (p Path) Base() string {
	return p.Path[strings.LastIndex(p.Path, "/")+1:]
}

// Parent returns the directory that contains the path ("" for the root).
func (p Path) Parent() string {
	if i := strings.LastIndex(p.Path, "/"); i >= 0 {
		return p.Path[:i]
	}
	return ""
}

// Join appends a relative path to dir; either may be empty.
func Join(dir, rel string) string {
	switch {
	case dir == "":
		return rel
	case rel == "":
		return dir
	}
	return dir + "/" + rel
}

// Rel returns path relative to dir, and false when path is not below dir.
func Rel(dir, path string) (string, bool) {
	if dir == "" {
		return path, path != ""
	}
	rest, ok := strings.CutPrefix(path, dir+"/")
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// LocalPath maps rel, a remote path relative to a downloaded directory, to a
// path below dest. It rejects everything that could leave dest or that the
// local system cannot store (FR-DOWN-5): absolute paths, "." and "..",
// control characters, and on Windows drive letters, reserved names and
// characters.
func LocalPath(dest, rel string) (string, error) {
	return localPath(dest, rel, runtime.GOOS)
}

var windowsReserved = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|CONIN\$|CONOUT\$|COM[0-9¹²³]|LPT[0-9¹²³])$`)

func localPath(dest, rel, goos string) (string, error) {
	if rel == "" || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("unsafe remote path %q", rel)
	}
	segs := strings.Split(rel, "/")
	for _, seg := range segs {
		if err := checkSegment(seg, goos); err != nil {
			return "", fmt.Errorf("unsafe remote path %q: %w", rel, err)
		}
	}
	root := filepath.Clean(dest)
	target := filepath.Join(append([]string{root}, segs...)...)
	if r, err := filepath.Rel(root, target); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", fmt.Errorf("unsafe remote path %q: it leaves %s", rel, dest)
	}
	return target, nil
}

func checkSegment(seg, goos string) error {
	switch seg {
	case "", ".", "..":
		return fmt.Errorf("%q segment", seg)
	}
	for _, r := range seg {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("control character in %q", seg)
		}
	}
	if goos != "windows" {
		return nil
	}
	if strings.ContainsAny(seg, `<>:"\|?*`) {
		return fmt.Errorf("%q contains a character that Windows does not allow in file names", seg)
	}
	if strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
		return fmt.Errorf("%q ends with a dot or a space, which Windows removes", seg)
	}
	stem, _, _ := strings.Cut(seg, ".")
	if windowsReserved.MatchString(strings.TrimRight(stem, " ")) {
		return fmt.Errorf("%q is a reserved name on Windows", seg)
	}
	return nil
}
