package files

import (
	"context"
	"iter"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/remote"
	"github.com/yand3r3d3v/nexr/internal/workpool"
)

// browse lists one level with the Browse API. ok is false when the server has
// no Browse API; the result is remembered.
func (s *Service) browse(ctx context.Context, repo, dir string) (nodes []nexus.BrowseNode, ok bool, err error) {
	s.mu.Lock()
	no := s.noBrowse
	s.mu.Unlock()
	if no {
		return nil, false, nil
	}
	nodes, err = s.api.Browse(ctx, repo, dir)
	if statusOf(err) == http.StatusNotFound {
		// The repository exists (callers resolve it first), so the endpoint
		// is missing, as on 3.71.
		s.mu.Lock()
		s.noBrowse = true
		s.mu.Unlock()
		return nil, false, nil
	}
	return nodes, err == nil, err
}

// ListDir lists one level of dir ("" is the root): directories and files,
// directories first, each group sorted by name. exists is false when dir has
// no entries (the root always exists). With meta, file entries carry sizes,
// times and checksums.
func (s *Service) ListDir(ctx context.Context, repo nexus.Repository, dir string, meta bool) (entries []Entry, exists bool, err error) {
	nodes, ok, err := s.browse(ctx, repo.Name, dir)
	if err != nil {
		return nil, false, err
	}
	if ok {
		var files []Entry
		for _, n := range nodes {
			p := remote.Join(dir, n.Name)
			if n.Folder {
				entries = append(entries, dirEntry(repo.Name, p))
			}
			if n.File {
				files = append(files, Entry{Repository: repo.Name, Path: p, Name: n.Name, Size: -1})
			}
		}
		if meta && len(files) > 0 {
			if err := s.fillMeta(ctx, repo, dir, files); err != nil {
				return nil, false, err
			}
		}
		entries = append(entries, files...)
	} else {
		// Without the Browse API, aggregate a recursive listing to one level.
		seen := map[string]bool{}
		for e, err := range s.Walk(ctx, repo, dir) {
			if err != nil {
				return nil, false, err
			}
			rel, _ := remote.Rel(dir, e.Path)
			name, _, deeper := strings.Cut(rel, "/")
			switch {
			case deeper && !seen[name+"/"]:
				seen[name+"/"] = true
				entries = append(entries, dirEntry(repo.Name, remote.Join(dir, name)))
			case !deeper:
				entries = append(entries, e)
			}
		}
	}
	SortEntries(entries)
	return entries, dir == "" || len(entries) > 0, nil
}

// SortEntries sorts directories before files, each by name.
func SortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return entries[i].Name < entries[j].Name
	})
}

// fillMeta completes the metadata of files that are directly in dir: from an
// exact group search for raw repositories, from HEAD requests otherwise and
// for files that the search index does not know yet.
func (s *Service) fillMeta(ctx context.Context, repo nexus.Repository, dir string, files []Entry) error {
	if strings.EqualFold(repo.Format, "raw") {
		byPath := map[string]nexus.Asset{}
		for a, err := range s.api.SearchAssets(ctx, nexus.AssetQuery{Repository: repo.Name, Group: nexus.QuotePath(dir)}) {
			if err != nil {
				if errs.Classify(err) == errs.KindRejected {
					break // fall back to HEAD requests
				}
				return err
			}
			byPath[a.Path] = a
		}
		for i := range files {
			if a, ok := byPath[files[i].Path]; ok {
				files[i] = assetEntry(a)
			}
		}
	}
	var missing []int
	for i := range files {
		if files[i].Size < 0 {
			missing = append(missing, i)
		}
	}
	var firstErr error
	workpool.Run(ctx, 8, missing, func(ctx context.Context, i int) error {
		info, err := s.api.Stat(ctx, repo.Name, files[i].Path)
		if err == nil {
			files[i] = statEntry(repo.Name, files[i].Path, info, s.api.ContentURL(repo.Name, files[i].Path))
		}
		return err
	}, func(_ int, err error) {
		if err != nil && !IsMissing(err) && firstErr == nil {
			firstErr = err
		}
	})
	return firstErr
}

func statEntry(repo, path string, info nexus.ContentInfo, url string) Entry {
	return Entry{
		Repository: repo, Path: path, Name: baseName(path), Size: info.Size,
		ContentType: info.ContentType, LastModified: info.LastModified,
		Checksum: nexus.Checksums{SHA1: info.SHA1}, DownloadURL: url,
	}
}

// Walk iterates over every file below dir ("" is the root), with metadata,
// in no particular order. It uses a prefix search where Nexus supports it,
// a traversal of the browse tree for names that break wildcard searches, and
// otherwise a scan of the whole repository.
func (s *Service) Walk(ctx context.Context, repo nexus.Repository, dir string) iter.Seq2[Entry, error] {
	return func(yield func(Entry, error) bool) {
		raw := strings.EqualFold(repo.Format, "raw")
		if raw && wildcardSafe(dir) {
			done, rejected := s.walkSearch(ctx, repo, dir, yield)
			if done || !rejected {
				return
			}
		}
		if raw && dir != "" {
			if _, ok, err := s.browse(ctx, repo.Name, dir); err == nil && ok {
				s.walkBrowse(ctx, repo, dir, yield)
				return
			}
		}
		s.walkScan(ctx, repo, dir, yield)
	}
}

// wildcardSafe reports whether dir works in an unquoted "group=/dir*" search:
// at least 3 characters before the wildcard (3.96) and only characters that
// the search does not split or interpret.
func wildcardSafe(dir string) bool {
	if len("/"+dir) < 3 {
		return false
	}
	for _, r := range dir {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_./", r) {
			return false
		}
	}
	return true
}

// walkSearch yields the files below dir from a group prefix search. rejected
// is true when the server refused the query before any result was yielded.
func (s *Service) walkSearch(ctx context.Context, repo nexus.Repository, dir string, yield func(Entry, error) bool) (done, rejected bool) {
	first := true
	for a, err := range s.api.SearchAssets(ctx, nexus.AssetQuery{Repository: repo.Name, Group: "/" + dir + "*"}) {
		if err != nil {
			if first && errs.Classify(err) == errs.KindRejected {
				return false, true
			}
			yield(Entry{}, err)
			return true, false
		}
		first = false
		// "/dir*" also matches "/dir-sibling"; keep only paths below dir.
		if _, ok := remote.Rel(dir, a.Path); ok {
			if !yield(assetEntry(a), nil) {
				return true, false
			}
		}
	}
	return true, false
}

// walkBrowse traverses the browse tree below dir and reads the metadata of
// each folder with an exact group search.
func (s *Service) walkBrowse(ctx context.Context, repo nexus.Repository, dir string, yield func(Entry, error) bool) {
	queue := []string{dir}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		entries, _, err := s.ListDir(ctx, repo, d, true)
		if err != nil {
			yield(Entry{}, err)
			return
		}
		for _, e := range entries {
			if e.Dir {
				queue = append(queue, e.Path)
				continue
			}
			if !yield(e, nil) {
				return
			}
		}
	}
}

// walkScan reads every asset of the repository and keeps those below dir.
func (s *Service) walkScan(ctx context.Context, repo nexus.Repository, dir string, yield func(Entry, error) bool) {
	for a, err := range s.api.Assets(ctx, repo.Name) {
		if err != nil {
			yield(Entry{}, err)
			return
		}
		if _, ok := remote.Rel(dir, a.Path); ok {
			if !yield(assetEntry(a), nil) {
				return
			}
		}
	}
}

// StatFile returns the file at path with its metadata, or ok=false when there
// is none (a directory is not a file).
func (s *Service) StatFile(ctx context.Context, repo nexus.Repository, path string) (e Entry, ok bool, err error) {
	info, err := s.api.Stat(ctx, repo.Name, path)
	if IsMissing(err) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	e = statEntry(repo.Name, path, info, s.api.ContentURL(repo.Name, path))
	if strings.EqualFold(repo.Format, "raw") {
		// Complete checksums, uploader and asset ID from the search index.
		for a, err := range s.api.SearchAssets(ctx, nexus.AssetQuery{Repository: repo.Name, Name: nexus.QuotePath(path)}) {
			if err == nil && a.Path == path {
				e = assetEntry(a)
			}
			break // the HEAD metadata is enough otherwise
		}
	}
	return e, true, nil
}

// DirExists reports whether there are entries below dir.
func (s *Service) DirExists(ctx context.Context, repo nexus.Repository, dir string) (bool, error) {
	if dir == "" {
		return true, nil
	}
	nodes, ok, err := s.browse(ctx, repo.Name, dir)
	if err != nil {
		return false, err
	}
	if ok {
		return len(nodes) > 0, nil
	}
	for _, err := range s.Walk(ctx, repo, dir) {
		return err == nil, err
	}
	return false, nil
}
