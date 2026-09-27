package nexustest

import (
	"crypto/sha1" //nolint:gosec // Nexus reports SHA-1 checksums
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Media types of image manifests.
const (
	OCIManifest = "application/vnd.oci.image.manifest.v1+json"
	OCIIndex    = "application/vnd.oci.image.index.v1+json"
)

// Image is one tag of an image in a docker or oci repository of the fake.
type Image struct {
	Name string // image name, e.g. "team/app"
	Tag  string
	// Digest of the manifest the tag points to; derived from Name and Tag
	// when empty.
	Digest       string
	MediaType    string    // defaults to OCIManifest
	Pushed       time.Time // defaults to the fake's clock
	Created      time.Time // build time; zero is reported like an image built without a date
	OS           string
	Architecture string
	TotalSize    int64
	// Unindexed keeps the tag out of search results, like a tag pushed a
	// moment ago. The Registry API and the components listing show it.
	Unindexed bool
}

// storedTag is a tag of a docker or oci repository: a component with one
// manifest asset.
type storedTag struct {
	Image
	repo           string
	componentID    string
	assetID        string
	seq            int
	lastDownloaded time.Time
}

// WithoutImageAttributes omits the docker attributes of manifest assets
// (build time, platform, size), as 3.71 does.
func WithoutImageAttributes() Option {
	return func(s *Server) { s.noImageAttrs = true }
}

// WithLegacyRegistryLinks makes registry pagination links point to /v2/…
// without the /repository/REPO prefix, as 3.71 does.
func WithLegacyRegistryLinks() Option {
	return func(s *Server) { s.legacyLinks = true }
}

// WithBearerTokenRealm answers anonymous registry requests with a Bearer
// challenge, as Nexus does with the Docker Bearer Token Realm. Anonymous
// tokens are issued at /v2/token while anonymous access is enabled.
func WithBearerTokenRealm() Option {
	return func(s *Server) { s.bearerRealm = true }
}

// PutImage stores an image tag directly, bypassing authentication. A tag
// that exists is moved to the new manifest.
func (s *Server) PutImage(repo string, img Image) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if img.MediaType == "" {
		img.MediaType = OCIManifest
	}
	if img.Digest == "" {
		img.Digest = digestOf(img.Name + ":" + img.Tag)
	}
	if img.Pushed.IsZero() {
		img.Pushed = s.now()
	}
	if s.images[repo] == nil {
		s.images[repo] = map[string]*storedTag{}
	}
	s.seq++
	id := func(kind string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s%08x", repo, kind, s.seq)))
	}
	s.images[repo][img.Name+":"+img.Tag] = &storedTag{
		Image: img, repo: repo, componentID: id("c"), assetID: id("a"), seq: s.seq,
	}
}

// DeleteImage deletes a tag directly.
func (s *Server) DeleteImage(repo, name, tag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.images[repo], name+":"+tag)
}

// SetIndexed adds a tag to the search index or removes it from it.
func (s *Server) SetIndexed(repo, name, tag string, indexed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.images[repo][name+":"+tag]; ok {
		t.Unindexed = !indexed
	}
}

// ImageTags returns the tags of an image, sorted.
func (s *Server) ImageTags(repo, name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, t := range s.images[repo] {
		if t.Name == name {
			out = append(out, t.Tag)
		}
	}
	sort.Strings(out)
	return out
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("sha256:%x", sum)
}

func isImageRepo(r Repo) bool { return r.Format == "docker" || r.Format == "oci" }

// sortedTags returns the tags of a repository in push order.
func (s *Server) sortedTags(repo string) []*storedTag {
	out := make([]*storedTag, 0, len(s.images[repo]))
	for _, t := range s.images[repo] {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

func (s *Server) componentJSON(t *storedTag) map[string]any {
	repo := s.repos[t.repo]
	path := "/v2/" + t.Name + "/manifests/" + t.Tag
	sha1sum := sha1.Sum([]byte(t.Digest)) //nolint:gosec // reported as Nexus does
	asset := map[string]any{
		"id": t.assetID, "repository": t.repo, "format": repo.Format, "path": path,
		"downloadUrl": s.BaseURL() + "/repository/" + t.repo + path,
		"checksum": map[string]string{
			"sha1":   fmt.Sprintf("%x", sha1sum),
			"sha256": strings.TrimPrefix(t.Digest, "sha256:"),
		},
		"contentType":    t.MediaType,
		"lastModified":   t.Pushed.Format("2006-01-02T15:04:05.000-07:00"),
		"lastDownloaded": nil,
		"uploader":       "admin",
		"uploaderIp":     "127.0.0.1",
		"fileSize":       401,
		"blobCreated":    t.Pushed.Format("2006-01-02T15:04:05.000-07:00"),
		"blobStoreName":  "default",
	}
	if !t.lastDownloaded.IsZero() {
		asset["lastDownloaded"] = t.lastDownloaded.Format("2006-01-02T15:04:05.000-07:00")
	}
	switch {
	case s.noImageAttrs:
	case repo.Format == "oci":
		asset["oci"] = map[string]any{"artifactType": "application/vnd.oci.image.config.v1+json", "content_digest": t.Digest}
	case t.MediaType == OCIIndex:
		// Nexus copies the attributes of one of the platforms.
		asset["docker"] = map[string]any{"content_digest": t.Digest, "os": t.OS, "architecture": t.Architecture}
	default:
		created := "0001-01-01T00:00:00Z"
		if !t.Created.IsZero() {
			created = t.Created.UTC().Format(time.RFC3339Nano)
		}
		asset["docker"] = map[string]any{
			"content_digest": t.Digest, "os": t.OS, "architecture": t.Architecture,
			"created": created, "totalSize": t.TotalSize,
		}
	}
	return map[string]any{
		"id": t.componentID, "repository": t.repo, "format": repo.Format, "group": nil,
		"name": t.Name, "version": t.Tag, "assets": []any{asset},
	}
}

// matchValue matches a search value: exact, or a prefix with a trailing "*".
func matchValue(q, v string) bool {
	if q == "" {
		return true
	}
	if p, ok := strings.CutSuffix(q, "*"); ok {
		return strings.HasPrefix(v, p)
	}
	return q == v
}

// searchComponents implements GET /v1/search for image tags, 50 per page.
func (s *Server) searchComponents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items := []map[string]any{}
	for _, t := range s.sortedTags(q.Get("repository")) {
		if !t.Unindexed && matchValue(q.Get("name"), t.Name) && matchValue(q.Get("version"), t.Tag) {
			items = append(items, s.componentJSON(t))
		}
	}
	s.page(w, r, items, 50)
}

// listComponents implements GET /v1/components, 10 per page.
func (s *Server) listComponents(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repository")
	if _, ok := s.repos[repo]; !ok {
		s.siesta(w, http.StatusNotFound)
		return
	}
	items := []map[string]any{}
	for _, t := range s.sortedTags(repo) {
		items = append(items, s.componentJSON(t))
	}
	s.page(w, r, items, 10)
}

func (s *Server) deleteComponent(w http.ResponseWriter, u user, id string) {
	if u.anonymous {
		s.siesta(w, http.StatusForbidden)
		return
	}
	for _, tags := range s.images {
		for key, t := range tags {
			if t.componentID == id {
				delete(tags, key)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	s.siesta(w, http.StatusNotFound)
}

// registry serves the Docker Registry v2 API of an image repository below
// /repository/REPO/v2/.
func (s *Server) registry(w http.ResponseWriter, r *http.Request, repo, path string) {
	if _, ok := s.registryUser(w, r, path); !ok {
		return
	}
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	rest := strings.TrimPrefix(strings.TrimPrefix(path, "v2"), "/")
	switch {
	case rest == "":
		writeJSON(w, http.StatusOK, map[string]any{})
	case rest == "_catalog":
		names := map[string]bool{}
		for _, t := range s.images[repo] {
			names[t.Name] = true
		}
		list := make([]string, 0, len(names))
		for n := range names {
			list = append(list, n)
		}
		items, next := s.registryPage(r, list)
		if next != "" {
			s.registryLink(w, r, repo, "_catalog", next)
		}
		writeJSON(w, http.StatusOK, map[string]any{"repositories": items})
	case strings.HasSuffix(rest, "/tags/list"):
		name := strings.TrimSuffix(rest, "/tags/list")
		var list []string
		for _, t := range s.images[repo] {
			if t.Name == name {
				list = append(list, t.Tag)
			}
		}
		if len(list) == 0 {
			registryError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
			return
		}
		items, next := s.registryPage(r, list)
		if next != "" {
			s.registryLink(w, r, repo, name+"/tags/list", next)
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "tags": items})
	case strings.Contains(rest, "/manifests/"):
		i := strings.LastIndex(rest, "/manifests/")
		name, ref := rest[:i], rest[i+len("/manifests/"):]
		var found *storedTag
		for _, t := range s.images[repo] {
			if t.Name == name && (t.Tag == ref || t.Digest == ref) {
				found = t
				break
			}
		}
		if found == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		}
		body := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q}`, found.MediaType)
		h := w.Header()
		h.Set("Docker-Content-Digest", found.Digest)
		h.Set("Content-Type", found.MediaType)
		h.Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			found.lastDownloaded = s.now()
			_, _ = w.Write([]byte(body))
		}
	default:
		registryError(w, http.StatusNotFound, "NOT_FOUND", "not found")
	}
}

// registryUser authenticates a registry request. With a Bearer token realm,
// anonymous clients are asked to fetch a token.
func (s *Server) registryUser(w http.ResponseWriter, r *http.Request, path string) (user, bool) {
	if s.bearerRealm {
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			if s.anonymous && strings.HasPrefix(tok, "anonymous ") {
				return user{name: "anonymous", anonymous: true}, true
			}
			registryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
			return user{}, false
		}
		if _, _, has := r.BasicAuth(); !has {
			realm := s.BaseURL() + "/v2/token"
			scope := "registry:catalog:*"
			if rest := strings.TrimPrefix(path, "v2/"); rest != "_catalog" && rest != "v2" && rest != "" {
				name := rest
				if i := strings.LastIndex(rest, "/tags/list"); i >= 0 {
					name = rest[:i]
				} else if i := strings.LastIndex(rest, "/manifests/"); i >= 0 {
					name = rest[:i]
				}
				scope = "repository:" + name + ":pull"
			}
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q,scope=%q`, realm, realm, scope))
			registryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "access to the requested resource is not authorized")
			return user{}, false
		}
	}
	return s.authenticate(w, r)
}

// token issues anonymous Bearer tokens (GET /v2/token).
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if !s.bearerRealm || !s.anonymous {
		registryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": "anonymous " + r.URL.Query().Get("scope")})
}

// registryPage applies the n and last parameters to a sorted list and
// returns the cursor of the next page, if there is one.
func (s *Server) registryPage(r *http.Request, list []string) (items []string, next string) {
	sort.Strings(list)
	q := r.URL.Query()
	if last := q.Get("last"); last != "" {
		i := sort.SearchStrings(list, last)
		for i < len(list) && list[i] <= last {
			i++
		}
		list = list[i:]
	}
	n, _ := strconv.Atoi(q.Get("n"))
	if n <= 0 || n >= len(list) {
		return list, ""
	}
	return list[:n], list[n-1]
}

func (s *Server) registryLink(w http.ResponseWriter, r *http.Request, repo, path, last string) {
	prefix := s.BaseURL() + "/repository/" + repo
	if s.legacyLinks {
		prefix = s.BaseURL()
	}
	q := url.Values{"n": {r.URL.Query().Get("n")}, "last": {last}}
	w.Header().Set("Link", fmt.Sprintf(`<%s/v2/%s?%s>; rel="next"`, prefix, path, q.Encode()))
}

func registryError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"errors": []any{map[string]any{"code": code, "message": message, "detail": nil}}})
}
