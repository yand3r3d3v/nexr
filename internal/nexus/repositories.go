package nexus

import (
	"context"
	"net/url"
	"sort"
	"strings"
)

// Repository is an entry of GET /v1/repositories.
type Repository struct {
	Name       string         `json:"name"`
	Format     string         `json:"format"`
	Type       string         `json:"type"`
	URL        string         `json:"url"`
	Online     *bool          `json:"online"` // not reported by older releases
	Attributes map[string]any `json:"attributes"`
}

// Repositories lists the repositories the user may browse, sorted by name.
func (c *Client) Repositories(ctx context.Context) ([]Repository, error) {
	var repos []Repository
	if err := c.getJSON(ctx, "/v1/repositories", nil, &repos); err != nil {
		return nil, err
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos, nil
}

// Repository returns one repository.
func (c *Client) Repository(ctx context.Context, name string) (Repository, error) {
	var repo Repository
	err := c.getJSON(ctx, "/v1/repositories/"+url.PathEscape(name), nil, &repo)
	return repo, err
}

// RepositorySettings returns the full configuration of a repository
// (GET /v1/repositories/{format}/{type}/{name}). It needs administrative read
// privileges.
func (c *Client) RepositorySettings(ctx context.Context, repo Repository) (map[string]any, error) {
	var settings map[string]any
	path := "/v1/repositories/" + apiFormat(repo.Format) + "/" + url.PathEscape(repo.Type) + "/" + url.PathEscape(repo.Name)
	err := c.getJSON(ctx, path, nil, &settings)
	return settings, err
}

// apiFormat maps a repository format to the path segment used by the
// repository management API ("maven2" is served under "maven").
func apiFormat(format string) string {
	if format == "maven2" {
		return "maven"
	}
	return strings.ReplaceAll(strings.ToLower(format), "-", "")
}
