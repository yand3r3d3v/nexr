package files

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/nexus"
	"github.com/yand3r3d3v/nexr/internal/remote"
)

// RemoveOptions control a removal.
type RemoveOptions struct {
	Include, Exclude []remote.Pattern
	Recursive        bool
	ServerSide       bool
	WaitTimeout      time.Duration // for server-side folder deletion
}

// RemoveItem is one deletion of a plan.
type RemoveItem struct {
	Repo    nexus.Repository
	Path    string
	AssetID string // for formats other than raw
	Folder  bool   // delete the folder on the server (--server-side)
	Missing bool   // the target does not exist
}

// Ref returns REPO/PATH.
func (it RemoveItem) Ref() string {
	if it.Folder {
		return it.Repo.Name + "/" + it.Path + "/"
	}
	return it.Repo.Name + "/" + it.Path
}

// RemovePlan lists what a removal deletes.
type RemovePlan struct {
	Items []RemoveItem
	// WholeRepos names repositories whose entire content is deleted, which
	// needs a stronger confirmation (FR-SAFE-4).
	WholeRepos []string
}

// Files returns the number of files the plan deletes directly.
func (p *RemovePlan) Files() int {
	n := 0
	for _, it := range p.Items {
		if !it.Missing && !it.Folder {
			n++
		}
	}
	return n
}

// PlanRemove resolves the targets (FR-RM-1). A directory needs Recursive; a
// path that is a file and a directory at the same time loses only the file
// without Recursive.
func (s *Service) PlanRemove(ctx context.Context, targets []remote.Path, opts RemoveOptions) (*RemovePlan, error) {
	plan := &RemovePlan{}
	seen := map[string]bool{}
	add := func(it RemoveItem) {
		key := fmt.Sprintf("%s\x00%s\x00%t", it.Repo.Name, it.Path, it.Folder)
		if !seen[key] {
			seen[key] = true
			plan.Items = append(plan.Items, it)
		}
	}
	for _, t := range targets {
		repo, err := s.Repository(ctx, t.Repo)
		if err != nil {
			return nil, err
		}
		if t.Path == "" {
			if !opts.Recursive {
				return nil, errs.Usage("%s is a repository; deleting all of its content needs -r", t.Repo)
			}
			plan.WholeRepos = append(plan.WholeRepos, t.Repo)
			if err := s.addFiles(ctx, repo, "", opts, add); err != nil {
				return nil, err
			}
			continue
		}
		var (
			file   Entry
			isFile bool
		)
		if !t.Dir {
			if file, isFile, err = s.StatFile(ctx, repo, t.Path); err != nil {
				return nil, err
			}
		}
		isDir, err := s.DirExists(ctx, repo, t.Path)
		if err != nil {
			return nil, err
		}
		switch {
		case isFile:
			add(RemoveItem{Repo: repo, Path: t.Path, AssetID: file.AssetID})
		case !isDir:
			add(RemoveItem{Repo: repo, Path: t.Path, Missing: true})
			continue
		}
		if !isDir {
			continue
		}
		if !opts.Recursive {
			if isFile {
				continue // the file only; its directory needs -r
			}
			return nil, errs.Usage("%s is a directory; deleting it needs -r", t).
				WithHint("run \"nexr ls %s/\" to see what it contains", t.Ref(t.Path))
		}
		if opts.ServerSide && len(opts.Include) == 0 && len(opts.Exclude) == 0 {
			add(RemoveItem{Repo: repo, Path: t.Path, Folder: true})
			continue
		}
		if err := s.addFiles(ctx, repo, t.Path, opts, add); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

func (s *Service) addFiles(ctx context.Context, repo nexus.Repository, dir string, opts RemoveOptions, add func(RemoveItem)) error {
	for e, err := range s.Walk(ctx, repo, dir) {
		if err != nil {
			return err
		}
		rel, _ := remote.Rel(dir, e.Path)
		if !remote.Selected(opts.Include, opts.Exclude, rel) {
			continue
		}
		add(RemoveItem{Repo: repo, Path: e.Path, AssetID: e.AssetID})
	}
	return nil
}

// Remove performs one deletion of a plan. It returns an error for which
// IsMissing is true when the target does not exist (any more).
func (s *Service) Remove(ctx context.Context, it RemoveItem, opts RemoveOptions) error {
	switch {
	case it.Missing:
		return errs.NotFound("%s not found", it.Ref())
	case it.Folder:
		return s.removeFolder(ctx, it, opts)
	case strings.EqualFold(it.Repo.Format, "raw"):
		// Needs only the delete privilege of the repository (FR-RM-3).
		return s.api.DeleteContent(ctx, it.Repo.Name, it.Path)
	case it.AssetID != "":
		return s.api.DeleteAsset(ctx, it.AssetID)
	}
	id, err := s.findAsset(ctx, it.Repo, it.Path)
	if err != nil {
		return err
	}
	return s.api.DeleteAsset(ctx, id)
}

// findAsset looks up the ID of an asset by scanning the repository.
func (s *Service) findAsset(ctx context.Context, repo nexus.Repository, path string) (string, error) {
	for a, err := range s.api.Assets(ctx, repo.Name) {
		if err != nil {
			return "", err
		}
		if a.Path == path {
			return a.ID, nil
		}
	}
	return "", errs.NotFound("%s/%s not found", repo.Name, path)
}

// removeFolder starts a server-side folder deletion and waits until the
// folder is gone (FR-RM-4).
func (s *Service) removeFolder(ctx context.Context, it RemoveItem, opts RemoveOptions) error {
	if err := s.api.DeleteFolder(ctx, it.Repo.Name, it.Path); err != nil {
		return err
	}
	wait := opts.WaitTimeout
	if wait <= 0 {
		wait = 10 * time.Minute
	}
	deadline := time.Now().Add(wait)
	delay := 250 * time.Millisecond
	for {
		exists, err := s.DirExists(ctx, it.Repo, it.Path)
		if err != nil || !exists {
			return err
		}
		if time.Now().After(deadline) {
			return errs.New(errs.KindTimeout, "%s still exists after %s; Nexus may still be deleting it", it.Ref(), wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(2*delay, 5*time.Second)
	}
}
