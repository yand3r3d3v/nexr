package files

import (
	"context"
	"crypto/sha1" //nolint:gosec // Nexus reports the SHA-1 of the content as ETag
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/remote"
)

// UploadOptions control an upload.
type UploadOptions struct {
	Include, Exclude []remote.Pattern
	FollowSymlinks   bool
	SkipExisting     bool
	Verify           bool
	Method           string        // "put" (default) or "components"
	ContentType      string        // content type of a single uploaded file
	OnBytes          func(n int64) // called as data is sent, for progress displays
}

// UploadItem is one file of an upload.
type UploadItem struct {
	Source string // the local path as shown to the user; "-" for stdin
	Local  string // the file to read; "" for stdin
	Path   string // the remote path
	Size   int64  // -1 for stdin
}

// UploadPlan is the list of files to upload.
type UploadPlan struct {
	Repo     nexus.Repository
	Items    []UploadItem
	Warnings []string
}

// PlanUpload checks the target repository and maps the sources to remote
// paths (FR-UP-1): a file goes into a directory target or to an exact path, a
// directory's contents go below the target, and "-" reads stdin.
func (s *Service) PlanUpload(ctx context.Context, sources []string, dest remote.Path, opts UploadOptions) (*UploadPlan, error) {
	repo, err := s.Repository(ctx, dest.Repo)
	if err != nil {
		return nil, err
	}
	if err := s.checkUploadTarget(ctx, repo); err != nil {
		return nil, err
	}
	plan := &UploadPlan{Repo: repo}
	if slices.Contains(sources, "-") {
		if len(sources) > 1 {
			return nil, errs.Usage("\"-\" (stdin) cannot be combined with other sources")
		}
		if dest.Dir {
			return nil, errs.Usage("uploading stdin needs the full remote path of the file, not a directory").
				WithHint("for example: nexr up - %s", dest.Ref(remote.Join(dest.Path, "file.bin")))
		}
		plan.Items = []UploadItem{{Source: "-", Path: dest.Path, Size: -1}}
		return plan, nil
	}
	if len(sources) > 1 && !dest.Dir {
		return nil, errs.Usage("with several sources the target must be a directory: end it with \"/\"").
			WithHint("for example: nexr up %s %s/", strings.Join(sources, " "), dest.Ref(dest.Path))
	}
	var problems []string
	for _, src := range sources {
		fi, err := os.Stat(src)
		if err != nil {
			kind := errs.KindGeneric
			if errors.Is(err, fs.ErrNotExist) {
				kind = errs.KindNotFound
			}
			return nil, errs.Wrap(kind, err, "cannot read %s", src)
		}
		switch {
		case fi.IsDir():
			w := &walker{opts: opts, plan: plan, dest: dest.Path, visited: map[string]bool{}}
			if err := w.walk(src, src, ""); err != nil {
				return nil, err
			}
			problems = append(problems, w.problems...)
		case fi.Mode().IsRegular():
			target := dest.Path
			if dest.Dir {
				target = remote.Join(dest.Path, filepath.Base(src))
			}
			if p := nameProblem(filepath.Base(src)); p != "" {
				problems = append(problems, src+": "+p)
				continue
			}
			plan.Items = append(plan.Items, UploadItem{Source: src, Local: src, Path: target, Size: fi.Size()})
		default:
			return nil, errs.Usage("%s is not a regular file or a directory", src)
		}
	}
	if len(problems) > 0 {
		return nil, errs.Usage("cannot upload %s", listProblems(problems)).
			WithHint("rename the files, or leave them out with --exclude")
	}
	if err := checkCollisions(plan.Items); err != nil {
		return nil, err
	}
	if opts.ContentType != "" && len(plan.Items) > 1 {
		return nil, errs.Usage("--content-type applies to a single file, but %d files would be uploaded", len(plan.Items))
	}
	return plan, nil
}

// checkUploadTarget allows hosted raw repositories only (FR-UP-3).
func (s *Service) checkUploadTarget(ctx context.Context, repo nexus.Repository) error {
	if !strings.EqualFold(repo.Format, "raw") {
		return errs.Usage("%s is a %s repository; nexr up supports raw repositories only", repo.Name, repo.Format).
			WithHint("uploads to other formats are planned; see the roadmap in docs/roadmap.md")
	}
	switch repo.Type {
	case "hosted":
		return nil
	case "group":
		e := errs.Usage("%s is a group repository; files can only be uploaded to a hosted repository", repo.Name)
		if members := s.hostedMembers(ctx, repo); len(members) > 0 {
			return e.WithHint("hosted members of %s: %s", repo.Name, strings.Join(members, ", "))
		}
		return e.WithHint("upload to one of the hosted repositories of the group")
	default:
		return errs.Usage("%s is a %s repository; files can only be uploaded to a hosted repository", repo.Name, repo.Type)
	}
}

// hostedMembers returns the hosted members of a group, when the user may read
// the group's configuration.
func (s *Service) hostedMembers(ctx context.Context, group nexus.Repository) []string {
	cfg, err := s.api.RepositorySettings(ctx, group)
	if err != nil {
		return nil
	}
	g, _ := cfg["group"].(map[string]any)
	names, _ := g["memberNames"].([]any)
	var hosted []string
	for _, n := range names {
		name, _ := n.(string)
		if r, err := s.Repository(ctx, name); err == nil && r.Type == "hosted" {
			hosted = append(hosted, name)
		}
	}
	sort.Strings(hosted)
	return hosted
}

// nameProblem reports names that Nexus would store under another path.
func nameProblem(name string) string {
	if runtime.GOOS != "windows" && strings.Contains(name, `\`) {
		return "the name contains \"\\\", which Nexus turns into a directory separator"
	}
	return ""
}

func listProblems(p []string) string {
	const show = 5
	if len(p) <= show {
		return strings.Join(p, "; ")
	}
	return strings.Join(p[:show], "; ") + fmt.Sprintf("; and %d more", len(p)-show)
}

func checkCollisions(items []UploadItem) error {
	seen := map[string]string{}
	var dups []string
	for _, it := range items {
		if prev, ok := seen[it.Path]; ok {
			dups = append(dups, fmt.Sprintf("%s and %s both map to %s", prev, it.Source, it.Path))
			continue
		}
		seen[it.Path] = it.Source
	}
	if len(dups) > 0 {
		return errs.Usage("two sources map to the same remote file: %s", listProblems(dups))
	}
	return nil
}

// walker collects the files of a directory tree.
type walker struct {
	opts     UploadOptions
	plan     *UploadPlan
	dest     string
	visited  map[string]bool // real paths of walked directories, against symlink loops
	problems []string
}

// walk adds the files below dir. shown is the path to show for dir; prefix
// is the relative path of dir below the upload root.
func (w *walker) walk(dir, shown, prefix string) error {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return errs.Wrap(errs.KindGeneric, err, "cannot read %s", shown)
	}
	if w.visited[resolved] {
		w.plan.Warnings = append(w.plan.Warnings, fmt.Sprintf("skipped %s: symbolic link loop", shown))
		return nil
	}
	w.visited[resolved] = true
	defer delete(w.visited, resolved)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return errs.Wrap(errs.KindGeneric, err, "cannot read %s", shown)
	}
	for _, de := range entries {
		name := de.Name()
		local := filepath.Join(dir, name)
		display := filepath.Join(shown, name)
		rel := remote.Join(prefix, name)
		mode := de.Type()
		if mode&fs.ModeSymlink != 0 {
			if !w.opts.FollowSymlinks {
				w.plan.Warnings = append(w.plan.Warnings, fmt.Sprintf("skipped symbolic link %s (use --follow-symlinks to follow it)", display))
				continue
			}
			fi, err := os.Stat(local)
			if err != nil {
				w.plan.Warnings = append(w.plan.Warnings, fmt.Sprintf("skipped broken symbolic link %s", display))
				continue
			}
			mode = fi.Mode().Type()
		}
		switch {
		case mode.IsDir():
			if remote.MatchAny(w.opts.Exclude, rel) {
				continue // an excluded directory is not walked
			}
			if err := w.walk(local, display, rel); err != nil {
				return err
			}
		case mode.IsRegular():
			if !remote.Selected(w.opts.Include, w.opts.Exclude, rel) {
				continue
			}
			if p := nameProblem(name); p != "" {
				w.problems = append(w.problems, display+": "+p)
				continue
			}
			fi, err := os.Stat(local)
			if err != nil {
				return errs.Wrap(errs.KindGeneric, err, "cannot read %s", display)
			}
			w.plan.Items = append(w.plan.Items, UploadItem{Source: display, Local: local, Path: remote.Join(w.dest, rel), Size: fi.Size()})
		default:
			w.plan.Warnings = append(w.plan.Warnings, fmt.Sprintf("skipped %s: not a regular file", display))
		}
	}
	return nil
}

// Upload uploads one item of a plan and returns the number of bytes sent.
// A file that exists already is skipped with --skip-existing.
func (s *Service) Upload(ctx context.Context, plan *UploadPlan, item UploadItem, opts UploadOptions, stdin io.Reader) (int64, error) {
	repo := plan.Repo.Name
	if opts.SkipExisting {
		if _, err := s.api.Stat(ctx, repo, item.Path); err == nil {
			return 0, &SkipError{Reason: "exists"}
		} else if !IsMissing(err) {
			return 0, err
		}
	}
	ct := opts.ContentType
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(item.Path))
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	var (
		hasher  hash.Hash
		counter = &countingReader{onBytes: opts.OnBytes}
		body    = nexus.UploadBody{ContentType: ct}
	)
	if item.Local == "" {
		body.Size, body.Replayable = -1, false
		body.Open = func() (io.ReadCloser, error) {
			hasher = sha1.New() //nolint:gosec // compared with the ETag
			counter.r = io.TeeReader(stdin, hasher)
			return io.NopCloser(counter), nil
		}
	} else {
		fi, err := os.Stat(item.Local)
		if err != nil {
			return 0, errs.Wrap(errs.KindGeneric, err, "cannot read %s", item.Source)
		}
		body.Size, body.Replayable = fi.Size(), true
		body.Open = func() (io.ReadCloser, error) {
			f, err := os.Open(item.Local)
			if err != nil {
				return nil, err
			}
			hasher = sha1.New() //nolint:gosec // compared with the ETag
			counter.n = 0
			counter.r = io.TeeReader(f, hasher)
			return struct {
				io.Reader
				io.Closer
			}{counter, f}, nil
		}
	}
	var err error
	if opts.Method == "components" {
		dir, name := "", item.Path
		if i := strings.LastIndex(item.Path, "/"); i >= 0 {
			dir, name = item.Path[:i], item.Path[i+1:]
		}
		err = s.api.UploadRawComponent(ctx, repo, dir, name, body)
	} else {
		err = s.api.Upload(ctx, repo, item.Path, body)
	}
	if err != nil {
		return 0, err
	}
	n := body.Size
	if n < 0 {
		n = counter.n
	}
	if opts.Verify {
		if hasher == nil { // an empty file is never opened
			hasher = sha1.New() //nolint:gosec // compared with the ETag
		}
		info, err := s.api.Stat(ctx, repo, item.Path)
		if err != nil {
			return n, fmt.Errorf("uploaded, but cannot verify: %w", err)
		}
		if local := hex.EncodeToString(hasher.Sum(nil)); info.SHA1 != "" && info.SHA1 != local {
			return n, errs.New(errs.KindGeneric, "uploaded, but the checksum differs: local SHA-1 %s, stored %s", local, info.SHA1)
		}
	}
	return n, nil
}

type countingReader struct {
	r       io.Reader
	n       int64
	onBytes func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if n > 0 && c.onBytes != nil {
		c.onBytes(int64(n))
	}
	return n, err
}
