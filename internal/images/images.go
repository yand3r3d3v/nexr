// Package images implements the operations on container images in docker and
// oci repositories (docs/architecture.md §5.8). The Registry API names images
// and tags; the search index adds push times and component IDs, and tags are
// deleted through their components, so that other tags of the same manifest
// stay intact.
package images

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strings"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/registry"
	"github.com/yand3r3d3v/nexr/internal/workpool"
)

// API is what the images domain needs from the Nexus REST API. *nexus.Client
// implements it.
type API interface {
	Repository(ctx context.Context, name string) (nexus.Repository, error)
	SearchComponents(ctx context.Context, q nexus.ComponentQuery) iter.Seq2[nexus.Component, error]
	Components(ctx context.Context, repo string) iter.Seq2[nexus.Component, error]
	DeleteComponent(ctx context.Context, id string) error
}

// Registry is what the images domain needs from the Registry API of the
// repository. *registry.Client implements it.
type Registry interface {
	Endpoint() string
	Catalog(ctx context.Context) iter.Seq2[string, error]
	Tags(ctx context.Context, image string) iter.Seq2[string, error]
	Head(ctx context.Context, image, ref string) (registry.Descriptor, error)
}

// IsImageFormat reports whether a repository format holds container images.
func IsImageFormat(format string) bool {
	f := strings.ToLower(format)
	return f == "docker" || f == "oci"
}

// Service works on the images of one repository.
type Service struct {
	api  API
	reg  Registry
	repo nexus.Repository
	// Warn reports problems that do not stop an operation, such as a
	// registry endpoint that fails while the search index answers.
	Warn func(format string, args ...any)

	stats map[string]Stats // read once
}

// Open returns a service for the repository name, which must hold images.
func Open(ctx context.Context, api API, reg Registry, name string) (*Service, error) {
	repo, err := api.Repository(ctx, name)
	if err != nil {
		if errs.Classify(err) == errs.KindNotFound {
			return nil, errs.Wrap(errs.KindNotFound, err, "repository %q not found", name).
				WithHint("run \"nexr repos --format docker\" to list the Docker repositories you may browse")
		}
		return nil, err
	}
	if !IsImageFormat(repo.Format) {
		return nil, errs.Usage("repository %s has the format %s; nexr docker works with docker and oci repositories", name, repo.Format).
			WithHint("use nexr ls, up, down and rm for files")
	}
	return &Service{api: api, reg: reg, repo: repo, Warn: func(string, ...any) {}}, nil
}

// Repository returns the repository.
func (s *Service) Repository() nexus.Repository { return s.repo }

// Tag is a tag of an image with the metadata of the manifest it points to.
type Tag struct {
	Repository   string
	Image        string
	Name         string
	Digest       string
	MediaType    string
	Pushed       time.Time // zero when the tag is not in the search index yet
	Created      time.Time // build time; zero when unknown
	LastPulled   time.Time
	Size         int64 // bytes; -1 when unknown, and always for an index
	OS           string
	Architecture string
	Uploader     string
	ComponentID  string // empty when the tag is not in the search index yet
}

// Ref returns IMAGE:TAG.
func (t Tag) Ref() string { return t.Image + ":" + t.Name }

// IsIndex reports whether the tag points to a multi-platform index.
func (t Tag) IsIndex() bool { return registry.IsIndex(t.MediaType) }

func (s *Service) tagOf(c nexus.Component) Tag {
	t := Tag{Repository: s.repo.Name, Image: c.Name, Name: c.Version, ComponentID: c.ID, Size: -1}
	for _, a := range c.Assets {
		if !strings.Contains(a.Path, "/manifests/") {
			continue
		}
		t.MediaType, t.Pushed, t.LastPulled, t.Uploader = a.ContentType, a.LastModified, a.LastDownloaded, a.Uploader
		if img := a.Image; img != nil {
			t.Digest, t.Created = img.Digest, img.Created
			// For an index, Nexus records the attributes of one platform only.
			if !registry.IsIndex(a.ContentType) {
				t.Size, t.OS, t.Architecture = img.TotalSize, img.OS, img.Architecture
			}
		}
		break
	}
	return t
}

// Images returns the names of the images of the repository, sorted: from the
// Registry API catalog or, when the registry endpoint fails, from the
// components (FR-DLS-1).
func (s *Service) Images(ctx context.Context) ([]string, error) {
	var names []string
	var regErr error
	for name, err := range s.reg.Catalog(ctx) {
		if err != nil {
			regErr = err
			break
		}
		names = append(names, name)
	}
	if regErr == nil {
		sort.Strings(names)
		return names, nil
	}
	if ctx.Err() != nil {
		return nil, regErr
	}
	s.Warn("the registry endpoint %s failed (%v); listing images from the components instead", s.reg.Endpoint(), regErr)
	stats, err := s.Stats(ctx)
	if err != nil {
		return nil, err
	}
	names = names[:0]
	for name := range stats {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Stats is the number of tags of an image and its last push.
type Stats struct {
	Tags       int
	LastPushed time.Time
}

// Stats reads every component of the repository once and summarises the tags
// of each image (FR-DLS-2). The result is kept for later calls.
func (s *Service) Stats(ctx context.Context) (map[string]Stats, error) {
	if s.stats != nil {
		return s.stats, nil
	}
	out := map[string]Stats{}
	for c, err := range s.api.Components(ctx, s.repo.Name) {
		if err != nil {
			return nil, err
		}
		t := s.tagOf(c)
		st := out[t.Image]
		st.Tags++
		if t.Pushed.After(st.LastPushed) {
			st.LastPushed = t.Pushed
		}
		out[t.Image] = st
	}
	s.stats = out
	return out, nil
}

// Tags returns the tags of an image, sorted by name. The Registry API decides
// which tags exist; the search index adds their metadata. Tags that are not
// indexed yet (pushed seconds ago) have no push time and no component ID; their
// digest and media type come from the registry (FR-DTAGS-2). If the registry
// endpoint fails, the search index alone is used. An image without tags is a
// not-found error.
func (s *Service) Tags(ctx context.Context, image string) ([]Tag, error) {
	indexed := map[string]Tag{}
	for c, err := range s.api.SearchComponents(ctx, nexus.ComponentQuery{Repository: s.repo.Name, Name: image}) {
		if err != nil {
			return nil, err
		}
		if c.Name == image {
			indexed[c.Version] = s.tagOf(c)
		}
	}
	var names []string
	var regErr error
	for name, err := range s.reg.Tags(ctx, image) {
		if err != nil {
			regErr = err
			break
		}
		names = append(names, name)
	}
	var tags []Tag
	switch {
	case regErr == nil:
		var missing []int
		for _, name := range names {
			t, ok := indexed[name]
			if !ok {
				t = Tag{Repository: s.repo.Name, Image: image, Name: name, Size: -1}
				missing = append(missing, len(tags))
			}
			tags = append(tags, t)
		}
		s.describe(ctx, tags, missing)
	case nameUnknown(regErr):
		// Indexed tags of an image the registry does not know were deleted
		// moments ago: the index lags behind.
		return nil, s.imageNotFound(image)
	case ctx.Err() != nil:
		return nil, regErr
	default:
		s.Warn("the registry endpoint %s failed (%v); tags pushed in the last seconds may be missing", s.reg.Endpoint(), regErr)
		for _, t := range indexed {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		return nil, s.imageNotFound(image)
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
	return tags, nil
}

func (s *Service) imageNotFound(image string) error {
	return errs.NotFound("image %s not found in %s", image, s.repo.Name).
		WithHint("run \"nexr docker ls -R %s\" to list its images", s.repo.Name)
}

// nameUnknown reports the registry's answer for an image it does not know. A
// plain 404 means something else, such as a wrong registry endpoint.
func nameUnknown(err error) bool {
	var re *registry.Error
	return errors.As(err, &re) && re.Code == "NAME_UNKNOWN"
}

// describe completes the digest and media type of the tags at the given
// positions from the registry.
func (s *Service) describe(ctx context.Context, tags []Tag, positions []int) {
	workpool.Run(ctx, 8, positions, func(ctx context.Context, i int) error {
		d, err := s.reg.Head(ctx, tags[i].Image, tags[i].Name)
		if err == nil {
			tags[i].Digest, tags[i].MediaType = d.Digest, d.MediaType
		}
		return err
	}, func(int, error) {})
}

// FindTag returns one tag of an image, with its component. A tag that is not
// in the search index yet is looked up in the components, which are always up
// to date. ok is false when the image has no such tag.
func (s *Service) FindTag(ctx context.Context, image, tag string) (t Tag, ok bool, err error) {
	q := nexus.ComponentQuery{Repository: s.repo.Name, Name: image, Version: tag}
	for c, err := range s.api.SearchComponents(ctx, q) {
		if err != nil {
			return Tag{}, false, err
		}
		if c.Name == image && c.Version == tag {
			return s.tagOf(c), true, nil
		}
	}
	// Not indexed: ask the registry whether the tag exists at all, which is
	// cheaper than reading every component.
	if exists, err := s.registryHasTag(ctx, image, tag); err == nil && !exists {
		return Tag{}, false, nil
	}
	for c, err := range s.api.Components(ctx, s.repo.Name) {
		if err != nil {
			return Tag{}, false, err
		}
		if c.Name == image && c.Version == tag {
			return s.tagOf(c), true, nil
		}
	}
	return Tag{}, false, nil
}

func (s *Service) registryHasTag(ctx context.Context, image, tag string) (bool, error) {
	for name, err := range s.reg.Tags(ctx, image) {
		switch {
		case nameUnknown(err):
			return false, nil
		case err != nil:
			return false, err
		case name == tag:
			return true, nil
		}
	}
	return false, nil
}

// ErrChanged reports a tag that points to another manifest than planned.
var ErrChanged = errors.New("the tag was pushed again since it was listed")

// DeleteTag deletes a tag through its component. Only this tag is removed:
// other tags of the same manifest, and manifests referenced by an index, stay
// (FR-DRM-5). With verify, the tag is first checked to still point to
// t.Digest, so that a tag pushed again after the listing is not deleted. A tag
// that no longer exists gives an error for which IsMissing is true.
func (s *Service) DeleteTag(ctx context.Context, t Tag, verify bool) error {
	if t.ComponentID == "" {
		return fmt.Errorf("%s is not in the search index yet", t.Ref())
	}
	if verify && t.Digest != "" {
		d, err := s.reg.Head(ctx, t.Image, t.Name)
		switch {
		case errs.Classify(err) == errs.KindNotFound:
			return missing(t)
		case err != nil:
			return errs.Wrap(errs.Classify(err), err, "cannot check %s before deleting it", t.Ref())
		case d.Digest != "" && d.Digest != t.Digest:
			return fmt.Errorf("%s now points to %s: %w", t.Ref(), d.Digest, ErrChanged)
		}
	}
	err := s.api.DeleteComponent(ctx, t.ComponentID)
	if errs.Classify(err) == errs.KindNotFound {
		return missing(t)
	}
	return err
}

type missingError struct{ ref string }

func (e *missingError) Error() string   { return e.ref + " not found" }
func (e *missingError) Kind() errs.Kind { return errs.KindNotFound }

func missing(t Tag) error { return &missingError{ref: t.Ref()} }

// IsMissing reports whether err says that a tag no longer exists.
func IsMissing(err error) bool {
	var m *missingError
	return errors.As(err, &m)
}
