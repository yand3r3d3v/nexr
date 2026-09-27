// Package files implements the path-addressed operations of nexr: listing,
// upload, download and removal (docs/architecture.md §5.6).
package files

import (
	"context"
	"errors"
	"io"
	"iter"
	"net/http"
	"sync"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/nexus"
)

// API is what the files domain needs from Nexus. *nexus.Client implements it.
type API interface {
	Repository(ctx context.Context, name string) (nexus.Repository, error)
	RepositorySettings(ctx context.Context, repo nexus.Repository) (map[string]any, error)
	Browse(ctx context.Context, repo, dir string) ([]nexus.BrowseNode, error)
	SearchAssets(ctx context.Context, q nexus.AssetQuery) iter.Seq2[nexus.Asset, error]
	Assets(ctx context.Context, repo string) iter.Seq2[nexus.Asset, error]
	Stat(ctx context.Context, repo, path string) (nexus.ContentInfo, error)
	Download(ctx context.Context, repo, path string) (io.ReadCloser, nexus.ContentInfo, error)
	Upload(ctx context.Context, repo, path string, body nexus.UploadBody) error
	UploadRawComponent(ctx context.Context, repo, dir, name string, body nexus.UploadBody) error
	DeleteContent(ctx context.Context, repo, path string) error
	DeleteAsset(ctx context.Context, id string) error
	DeleteFolder(ctx context.Context, repo, dir string) error
	ContentURL(repo, path string) string
}

// Entry is a file or a directory of a repository.
type Entry struct {
	Repository     string
	Path           string // full path in the repository, without a leading slash
	Name           string
	Dir            bool
	Size           int64 // -1 when unknown
	ContentType    string
	LastModified   time.Time
	BlobCreated    time.Time
	LastDownloaded time.Time
	Uploader       string
	Checksum       nexus.Checksums
	AssetID        string
	DownloadURL    string
}

func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

func assetEntry(a nexus.Asset) Entry {
	return Entry{
		Repository: a.Repository, Path: a.Path, Name: baseName(a.Path), Size: a.Size,
		ContentType: a.ContentType, LastModified: a.LastModified, BlobCreated: a.BlobCreated,
		LastDownloaded: a.LastDownloaded, Uploader: a.Uploader, Checksum: a.Checksum,
		AssetID: a.ID, DownloadURL: a.DownloadURL,
	}
}

func dirEntry(repo, path string) Entry {
	return Entry{Repository: repo, Path: path, Name: baseName(path), Dir: true, Size: -1}
}

// Service runs the operations against one Nexus instance.
type Service struct {
	api API

	mu       sync.Mutex
	repos    map[string]nexus.Repository
	noBrowse bool // learned on first use: the server has no Browse API
}

// New returns a service that uses api.
func New(api API) *Service {
	return &Service{api: api, repos: map[string]nexus.Repository{}}
}

// Repository returns a repository, or a not-found error with a hint.
func (s *Service) Repository(ctx context.Context, name string) (nexus.Repository, error) {
	s.mu.Lock()
	r, ok := s.repos[name]
	s.mu.Unlock()
	if ok {
		return r, nil
	}
	r, err := s.api.Repository(ctx, name)
	if err != nil {
		if errs.Classify(err) == errs.KindNotFound {
			return r, errs.Wrap(errs.KindNotFound, err, "repository %q not found", name).
				WithHint("run \"nexr repos\" to list the repositories you may browse")
		}
		return r, err
	}
	s.mu.Lock()
	s.repos[name] = r
	s.mu.Unlock()
	return r, nil
}

func statusOf(err error) int {
	var apiErr *nexus.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// SkipError reports an item that was deliberately not processed.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return "skipped: " + e.Reason }

// IsSkip reports whether err is a *SkipError.
func IsSkip(err error) bool {
	var s *SkipError
	return errors.As(err, &s)
}

// IsMissing reports whether err means that the item does not exist (404).
func IsMissing(err error) bool {
	return statusOf(err) == http.StatusNotFound
}
