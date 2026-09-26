package files

import (
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // Nexus reports the SHA-1 of the content as ETag
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/remote"
)

// DownloadOptions control a download.
type DownloadOptions struct {
	Include, Exclude []remote.Pattern
	SkipExisting     bool
	NoVerify         bool
	OnBytes          func(n int64) // called as data arrives, for progress displays
}

// DownloadItem is one file of a download.
type DownloadItem struct {
	Path         string // remote path
	Rel          string // path relative to the downloaded directory
	Local        string // local file; "" for stdout
	Size         int64
	SHA256, SHA1 string // expected checksums, when known
	LastModified time.Time
	Err          error // the file cannot be downloaded, e.g. an unsafe name
}

// DownloadPlan is the list of files to download.
type DownloadPlan struct {
	Repo     nexus.Repository
	Items    []DownloadItem
	ToStdout bool
}

// PlanDownload resolves src (FR-DOWN-1) and maps its files to local paths
// (FR-DOWN-2). dest is "" for the current directory, "-" for stdout, an
// existing directory, a path ending in a separator, or a file name.
func (s *Service) PlanDownload(ctx context.Context, src remote.Path, dest string, opts DownloadOptions) (*DownloadPlan, error) {
	repo, err := s.Repository(ctx, src.Repo)
	if err != nil {
		return nil, err
	}
	plan := &DownloadPlan{Repo: repo}
	if !src.Dir {
		e, ok, err := s.StatFile(ctx, repo, src.Path)
		if err != nil {
			return nil, err
		}
		if ok {
			item := itemFor(e, e.Name)
			switch {
			case dest == "-":
				plan.ToStdout = true
			case dest == "" || isDirTarget(dest):
				item.Local, item.Err = remote.LocalPath(orDot(dest), e.Name)
			default:
				item.Local = dest
			}
			plan.Items = []DownloadItem{item}
			return plan, nil
		}
	}
	exists, err := s.DirExists(ctx, repo, src.Path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errs.NotFound("nothing found at %s", src).
			WithHint("the search index can lag a few seconds behind uploads; run \"nexr ls %s\" to check", src.Ref(src.Parent()))
	}
	if dest == "-" {
		return nil, errs.Usage("%s is a directory and cannot be written to stdout", src)
	}
	root := orDot(dest)
	for e, err := range s.Walk(ctx, repo, src.Path) {
		if err != nil {
			return nil, err
		}
		rel, _ := remote.Rel(src.Path, e.Path)
		if !remote.Selected(opts.Include, opts.Exclude, rel) {
			continue
		}
		item := itemFor(e, rel)
		item.Local, item.Err = remote.LocalPath(root, rel)
		plan.Items = append(plan.Items, item)
	}
	sort.Slice(plan.Items, func(i, j int) bool { return plan.Items[i].Rel < plan.Items[j].Rel })
	return plan, nil
}

func itemFor(e Entry, rel string) DownloadItem {
	return DownloadItem{
		Path: e.Path, Rel: rel, Size: e.Size,
		SHA256: e.Checksum.SHA256, SHA1: e.Checksum.SHA1, LastModified: e.LastModified,
	}
}

func orDot(dest string) string {
	if dest == "" {
		return "."
	}
	return dest
}

// isDirTarget reports whether dest names a directory: it exists as one, or
// ends with a path separator.
func isDirTarget(dest string) bool {
	if strings.HasSuffix(dest, "/") || strings.HasSuffix(dest, string(filepath.Separator)) {
		return true
	}
	fi, err := os.Stat(dest)
	return err == nil && fi.IsDir()
}

// Download downloads one item of a plan and returns the number of bytes
// received. Files are written to a temporary file next to the target and
// renamed into place only after they were verified (FR-DOWN-3, FR-DOWN-4).
func (s *Service) Download(ctx context.Context, plan *DownloadPlan, item DownloadItem, opts DownloadOptions, stdout io.Writer) (n int64, err error) {
	if item.Err != nil {
		return 0, errs.Wrap(errs.KindRejected, item.Err, "not downloaded")
	}
	if plan.ToStdout {
		n, _, err := s.fetch(ctx, plan.Repo.Name, item, opts, stdout)
		return n, err
	}
	if opts.SkipExisting {
		if _, err := os.Lstat(item.Local); err == nil {
			return 0, &SkipError{Reason: "exists locally"}
		}
	}
	dir := filepath.Dir(item.Local)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // usual permissions for downloads; the umask applies
		return 0, errs.Wrap(errs.KindGeneric, err, "cannot create %s", dir)
	}
	tmp, err := createTemp(dir)
	if err != nil {
		return 0, errs.Wrap(errs.KindGeneric, err, "cannot write to %s", dir)
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	n, info, err := s.fetch(ctx, plan.Repo.Name, item, opts, tmp)
	if err != nil {
		return n, err
	}
	if err := tmp.Sync(); err != nil {
		return n, err
	}
	if err := tmp.Close(); err != nil {
		return n, err
	}
	// FR-DOWN-6: the local modification time is the remote one.
	if mtime := item.LastModified; !mtime.IsZero() {
		_ = os.Chtimes(tmp.Name(), mtime, mtime)
	} else if mtime := info.LastModified; !mtime.IsZero() {
		_ = os.Chtimes(tmp.Name(), mtime, mtime)
	}
	if fi, err := os.Stat(item.Local); err == nil && fi.IsDir() {
		return n, errs.New(errs.KindGeneric, "cannot write %s: it is a directory", item.Local)
	}
	if err := os.Rename(tmp.Name(), item.Local); err != nil {
		return n, errs.Wrap(errs.KindGeneric, err, "cannot write %s", item.Local)
	}
	done = true
	return n, nil
}

// fetch copies a remote file to w and verifies it against the SHA-256 from
// the listing, or else the SHA-1 that Nexus sends as ETag.
func (s *Service) fetch(ctx context.Context, repo string, item DownloadItem, opts DownloadOptions, w io.Writer) (int64, nexus.ContentInfo, error) {
	rc, info, err := s.api.Download(ctx, repo, item.Path)
	if err != nil {
		return 0, info, err
	}
	defer rc.Close()
	h256, h1 := sha256.New(), sha1.New() //nolint:gosec // compared with the ETag
	var src io.Reader = rc
	if opts.OnBytes != nil {
		src = &countingReader{r: rc, onBytes: opts.OnBytes}
	}
	n, err := io.Copy(io.MultiWriter(w, h256, h1), src)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return n, info, errs.Wrap(errs.KindGeneric, err, "cannot write")
		}
		return n, info, err
	}
	if info.Size >= 0 && n != info.Size {
		return n, info, errs.New(errs.KindNetwork, "incomplete download: %d of %d bytes", n, info.Size)
	}
	if opts.NoVerify {
		return n, info, nil
	}
	want1 := info.SHA1
	if want1 == "" {
		want1 = item.SHA1
	}
	switch {
	case item.SHA256 != "":
		if got := hex.EncodeToString(h256.Sum(nil)); got != item.SHA256 {
			return n, info, checksumError("SHA-256", got, item.SHA256)
		}
	case want1 != "":
		if got := hex.EncodeToString(h1.Sum(nil)); got != want1 {
			return n, info, checksumError("SHA-1", got, want1)
		}
	}
	return n, info, nil
}

func checksumError(alg, got, want string) error {
	return errs.New(errs.KindGeneric, "checksum mismatch: %s %s, expected %s", alg, got, want).
		WithHint("the file was discarded; try again, or use --no-verify to keep unverified files")
}

// createTemp creates a temporary file next to the target. Unlike os.CreateTemp
// it uses the permissions 0666 minus the umask, which the final file keeps.
func createTemp(dir string) (*os.File, error) {
	for range 100 {
		var b [8]byte
		_, _ = rand.Read(b[:])
		name := filepath.Join(dir, ".nexr-"+hex.EncodeToString(b[:])+".part")
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666) //nolint:gosec // usual permissions for downloads; the umask applies
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
	return nil, errors.New("cannot create a temporary file")
}
