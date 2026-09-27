package nexus

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// BrowseNode is one entry of a repository's browse tree.
type BrowseNode struct {
	Name   string // the decoded name
	File   bool   // a file is stored at this path
	Folder bool   // paths exist below it (a path can be a file and a folder)
}

type browseJSON struct {
	Text string `json:"text"`
	Type string `json:"type"`
	Leaf bool   `json:"leaf"`
}

// Browse lists one level of the browse tree below dir ("" is the root) with
// GET /v1/repositories/{repo}/browse. The tree can lag a few seconds behind
// uploads. Releases without the Browse API (3.71) answer 404 even for an
// existing repository; unknown paths give an empty list.
func (c *Client) Browse(ctx context.Context, repo, dir string) ([]BrowseNode, error) {
	var nodes []browseJSON
	err := c.getJSON(ctx, "/v1/repositories/"+url.PathEscape(repo)+"/browse", url.Values{"path": {"/" + dir}}, &nodes)
	if err != nil {
		return nil, err
	}
	out := make([]BrowseNode, 0, len(nodes))
	for _, n := range nodes {
		// "id" is not used: it encodes spaces as "+" and doubles slashes.
		if n.Text == "" {
			continue
		}
		out = append(out, BrowseNode{Name: n.Text, File: strings.EqualFold(n.Type, "asset"), Folder: !n.Leaf})
	}
	return out, nil
}

// DeleteFolder starts the deletion of a folder and everything below it
// (DELETE /v1/repositories/{repo}/browse). Nexus deletes asynchronously; the
// folder disappears some time after this call returns.
func (c *Client) DeleteFolder(ctx context.Context, repo, dir string) error {
	return c.send(ctx, http.MethodDelete, c.URL("/service/rest/v1/repositories/"+url.PathEscape(repo)+"/browse", url.Values{"path": {"/" + dir}}))
}
