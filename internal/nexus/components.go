package nexus

import (
	"context"
	"iter"
	"net/http"
	"net/url"
)

// Component is a component with its assets. In docker and oci repositories a
// component is one tag of an image: Name is the image name, Version the tag,
// and the only asset is the manifest the tag points to.
type Component struct {
	ID         string
	Repository string
	Format     string
	Group      string
	Name       string
	Version    string
	Assets     []Asset
}

type componentJSON struct {
	ID         string      `json:"id"`
	Repository string      `json:"repository"`
	Format     string      `json:"format"`
	Group      string      `json:"group"`
	Name       string      `json:"name"`
	Version    string      `json:"version"`
	Assets     []assetJSON `json:"assets"`
}

func components(seq iter.Seq2[componentJSON, error]) iter.Seq2[Component, error] {
	return func(yield func(Component, error) bool) {
		for c, err := range seq {
			comp := Component{
				ID: c.ID, Repository: c.Repository, Format: c.Format,
				Group: c.Group, Name: c.Name, Version: c.Version,
			}
			for _, a := range c.Assets {
				comp.Assets = append(comp.Assets, a.asset())
			}
			if !yield(comp, err) || err != nil {
				return
			}
		}
	}
}

// Components iterates over every component of a repository
// (GET /v1/components). The result is always up to date, but the whole
// repository is read.
func (c *Client) Components(ctx context.Context, repo string) iter.Seq2[Component, error] {
	return components(Paginate[componentJSON](ctx, c, "/v1/components", url.Values{"repository": {repo}}))
}

// ComponentQuery selects components for SearchComponents. Empty fields match
// anything. Name and Version match exactly and case-sensitively; a trailing
// "*" is a wildcard.
type ComponentQuery struct {
	Repository string
	Name       string
	Version    string
}

// SearchComponents iterates over the components that match q (GET /v1/search).
// Search results can lag a few seconds behind uploads, and a repository that
// does not exist gives no results rather than an error.
func (c *Client) SearchComponents(ctx context.Context, q ComponentQuery) iter.Seq2[Component, error] {
	v := url.Values{"repository": {q.Repository}}
	if q.Name != "" {
		v.Set("name", q.Name)
	}
	if q.Version != "" {
		v.Set("version", q.Version)
	}
	return components(Paginate[componentJSON](ctx, c, "/v1/search", v))
}

// DeleteComponent deletes a component and its assets
// (DELETE /v1/components/{id}). For an image tag this removes only the tag:
// other tags that point to the same manifest stay intact.
func (c *Client) DeleteComponent(ctx context.Context, id string) error {
	return c.send(ctx, http.MethodDelete, c.URL("/service/rest/v1/components/"+url.PathEscape(id), nil))
}
