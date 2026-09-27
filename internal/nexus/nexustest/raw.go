package nexustest

import (
	"crypto/md5"  //nolint:gosec // Nexus reports MD5 checksums
	"crypto/sha1" //nolint:gosec // Nexus reports SHA-1 checksums
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// storedFile is a file of a raw repository.
type storedFile struct {
	id          string
	path        string // without a leading slash
	content     []byte
	contentType string
	uploader    string
	created     time.Time
	modified    time.Time
	seq         int
}

// group is the component group of a raw file: its directory, "/dir/sub", or
// "/" at the root.
func (f *storedFile) group() string {
	i := strings.LastIndex(f.path, "/")
	if i < 0 {
		return "/"
	}
	return "/" + f.path[:i]
}

// PutFile stores a file directly, bypassing authentication and write policy.
func (s *Server) PutFile(repo, path string, content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeFile(repo, strings.Trim(path, "/"), content, detectType(path, ""), "admin")
}

// File returns the content of a stored file.
func (s *Server) File(repo, path string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.files[repo][strings.TrimLeft(path, "/")]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), f.content...), true
}

// Paths returns the paths of all files of a repository, sorted.
func (s *Server) Paths(repo string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for p := range s.files[repo] {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (s *Server) storeFile(repo, path string, content []byte, contentType, uploader string) *storedFile {
	if s.files[repo] == nil {
		s.files[repo] = map[string]*storedFile{}
	}
	now := s.now()
	f, exists := s.files[repo][path]
	if !exists {
		s.seq++
		f = &storedFile{
			id:      base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%08x", repo, s.seq))),
			path:    path,
			created: now,
			seq:     s.seq,
		}
		s.files[repo][path] = f
	}
	f.content, f.contentType, f.uploader, f.modified = content, contentType, uploader, now
	return f
}

func (s *Server) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now().UTC().Truncate(time.Millisecond)
}

// contentPath extracts REPO and PATH from /repository/REPO/PATH the way Jetty
// does: an unencoded ";" starts a path parameter, which is dropped, and Nexus
// treats a backslash as a directory separator.
func (s *Server) contentPath(r *http.Request) (repo, path string, ok bool) {
	raw, _, _ := strings.Cut(r.RequestURI, "?")
	raw, ok = strings.CutPrefix(raw, s.contextPath+"/repository/")
	if !ok {
		return "", "", false
	}
	segs := strings.Split(raw, "/")
	for i, seg := range segs {
		seg, _, _ = strings.Cut(seg, ";")
		dec, err := url.PathUnescape(seg)
		if err != nil {
			return "", "", false
		}
		segs[i] = dec
	}
	path = strings.ReplaceAll(strings.Join(segs[1:], "/"), `\`, "/")
	return segs[0], strings.Trim(path, "/"), true
}

func (s *Server) content(w http.ResponseWriter, r *http.Request) {
	repoName, path, ok := s.contentPath(r)
	if !ok {
		s.htmlNotFound(w)
		return
	}
	u, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	repo, found := s.repos[repoName]
	if !found {
		textError(w, http.StatusNotFound, "Repository not found")
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		f, found := s.files[repoName][path]
		if !found || path == "" {
			textError(w, http.StatusNotFound, "/"+path)
			return
		}
		h := w.Header()
		h.Set("ETag", `"`+sum(sha1.New(), f.content)+`"`) //nolint:gosec // Nexus's ETag is a SHA-1
		h.Set("Last-Modified", f.modified.Format(http.TimeFormat))
		h.Set("Content-Type", f.contentType)
		h.Set("Content-Length", strconv.Itoa(len(f.content)))
		h.Set("Content-Disposition", "attachment")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(f.content)
		}
	case http.MethodPut:
		if !s.mayWrite(w, u, repo, path) {
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			textError(w, http.StatusBadRequest, err.Error())
			return
		}
		ct := r.Header.Get("Content-Type")
		if mt, _, _ := mime.ParseMediaType(ct); mt == "application/x-www-form-urlencoded" {
			body = nil // Jetty takes a form body for request parameters
		}
		s.storeFile(repoName, path, body, detectType(path, ct), u.name)
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		if u.anonymous {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if _, found := s.files[repoName][path]; !found {
			textError(w, http.StatusNotFound, "/"+path)
			return
		}
		delete(s.files[repoName], path)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// mayWrite checks the repository type and write policy, as Nexus does.
func (s *Server) mayWrite(w http.ResponseWriter, u user, repo Repo, path string) bool {
	switch {
	case u.anonymous:
		w.WriteHeader(http.StatusForbidden)
	case repo.Type != "hosted":
		textError(w, http.StatusMethodNotAllowed, "PUT")
	case repo.Format != "raw":
		textError(w, http.StatusBadRequest, "Invalid path for a "+repo.Format+" repository")
	case repo.WritePolicy == "DENY":
		textError(w, http.StatusBadRequest, repo.Name+"/"+path+" is read-only")
	case repo.WritePolicy == "ALLOW_ONCE" && s.files[repo.Name][path] != nil:
		textError(w, http.StatusConflict, repo.Name+"/"+path+" -  cannot be updated as asset already exists and redeploy is not allowed")
	default:
		return true
	}
	return false
}

func detectType(path, header string) string {
	if t := mime.TypeByExtension(pathExt(path)); t != "" {
		return t
	}
	if header != "" {
		return header
	}
	return "application/octet-stream"
}

func pathExt(p string) string {
	base := p[strings.LastIndex(p, "/")+1:]
	if i := strings.LastIndex(base, "."); i > 0 {
		return base[i:]
	}
	return ""
}

func sum(h interface {
	io.Writer
	Sum([]byte) []byte
}, b []byte) string {
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func textError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg) //nolint:gosec // test fake; messages are plain text
}

// assetJSON renders a file like GET /v1/assets on 3.96.
func (s *Server) assetJSON(repo string, f *storedFile) map[string]any {
	return map[string]any{
		"id": f.id, "repository": repo, "format": s.repos[repo].Format,
		"path":        "/" + f.path,
		"downloadUrl": s.BaseURL() + "/repository/" + repo + "/" + escapePath(f.path),
		"checksum": map[string]string{
			"sha1":   sum(sha1.New(), f.content), //nolint:gosec // reported as Nexus does
			"sha256": sum(sha256.New(), f.content),
			"sha512": sum(sha512.New(), f.content),
			"md5":    sum(md5.New(), f.content), //nolint:gosec // reported as Nexus does
		},
		"contentType":    f.contentType,
		"lastModified":   f.modified.Format("2006-01-02T15:04:05.000-07:00"),
		"lastDownloaded": nil,
		"uploader":       f.uploader,
		"uploaderIp":     "127.0.0.1",
		"fileSize":       len(f.content),
		"blobCreated":    f.created.Format("2006-01-02T15:04:05.000-07:00"),
		"blobStoreName":  "default",
	}
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// sortedFiles returns the files of a repository in creation order, as Nexus
// lists them.
func (s *Server) sortedFiles(repo string) []*storedFile {
	out := make([]*storedFile, 0, len(s.files[repo]))
	for _, f := range s.files[repo] {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// page writes one page of a continuation-token paginated list.
func (s *Server) page(w http.ResponseWriter, r *http.Request, items []map[string]any, size int) {
	start, _ := strconv.Atoi(r.URL.Query().Get("continuationToken"))
	if start < 0 || start > len(items) {
		start = len(items)
	}
	end := min(start+size, len(items))
	var token any
	if end < len(items) {
		token = strconv.Itoa(end)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items[start:end], "continuationToken": token})
}

// listAssets implements GET /v1/assets, 100 assets per page.
func (s *Server) listAssets(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repository")
	if _, ok := s.repos[repo]; !ok {
		s.siesta(w, http.StatusNotFound)
		return
	}
	var items []map[string]any
	for _, f := range s.sortedFiles(repo) {
		items = append(items, s.assetJSON(repo, f))
	}
	s.page(w, r, items, 100)
}

// searchAssets implements GET /v1/search/assets with the group rules of 3.96,
// 50 assets per page.
func (s *Server) searchAssets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo := q.Get("repository")
	if _, ok := s.repos[repo]; !ok {
		writeJSON(w, http.StatusBadRequest, []map[string]string{{"id": "repository", "message": "Unable to locate repository with name " + repo}})
		return
	}
	matchGroup, err := groupMatcher(q.Get("group"))
	if err != "" {
		writeJSON(w, http.StatusBadRequest, []map[string]string{{"id": "*", "message": err}})
		return
	}
	matchName, err := groupMatcher(q.Get("name"))
	if err != "" {
		writeJSON(w, http.StatusBadRequest, []map[string]string{{"id": "*", "message": err}})
		return
	}
	var items []map[string]any
	for _, f := range s.sortedFiles(repo) {
		if matchGroup(f.group()) && matchName("/"+f.path) {
			items = append(items, s.assetJSON(repo, f))
		}
	}
	s.page(w, r, items, 50)
}

// groupMatcher reproduces the matching of the group and name search
// parameters on 3.96 (docs/nexus-api.md, "Matching rules").
func groupMatcher(q string) (func(string) bool, string) {
	switch {
	case q == "":
		return func(string) bool { return true }, ""
	case len(q) >= 2 && q[0] == '"' && strings.LastIndex(q, `"`) > 0:
		// An exact phrase; a wildcard after the quotes changes nothing.
		inner := q[1:strings.LastIndex(q, `"`)]
		want := strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(inner)
		return func(g string) bool { return g == want }, ""
	case strings.ContainsAny(q, " \t"):
		return func(string) bool { return false }, "" // split into terms: false negatives
	case strings.HasSuffix(q, "*"):
		prefix := strings.TrimSuffix(q, "*")
		if len(prefix) < 3 {
			return nil, "3 characters or more are required with a trailing wildcard (*)"
		}
		return func(g string) bool { return strings.HasPrefix(g, prefix) }, ""
	default:
		return func(g string) bool { return g == q }, ""
	}
}

// browse implements GET /v1/repositories/{repo}/browse (one level).
func (s *Server) browse(w http.ResponseWriter, r *http.Request, repo string) {
	if _, ok := s.repos[repo]; !ok {
		s.siesta(w, http.StatusNotFound)
		return
	}
	if s.noBrowse {
		w.WriteHeader(http.StatusNotFound) // releases without the Browse API
		return
	}
	dir := strings.Trim(r.URL.Query().Get("path"), "/")
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	type node struct{ file, folder bool }
	nodes := map[string]*node{}
	for p := range s.files[repo] {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok || rest == "" {
			continue
		}
		name, below, deeper := strings.Cut(rest, "/")
		n := nodes[name]
		if n == nil {
			n = &node{}
			nodes[name] = n
		}
		if deeper && below != "" {
			n.folder = true
		} else {
			n.file = true
		}
	}
	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		n := nodes[name]
		typ := "folder"
		if n.file {
			typ = "asset"
		}
		out = append(out, map[string]any{
			// Like Nexus, the id encodes spaces as "+"; clients must use text.
			"id":   url.QueryEscape(prefix + name),
			"text": name, "type": typ, "leaf": !n.folder,
			"componentId": nil, "assetId": nil, "packageUrl": nil,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteFolder implements DELETE /v1/repositories/{repo}/browse. Nexus
// deletes asynchronously; the fake deletes at once.
func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request, u user, repo string) {
	if !u.admin {
		s.siesta(w, http.StatusForbidden)
		return
	}
	if _, ok := s.repos[repo]; !ok {
		s.siesta(w, http.StatusNotFound)
		return
	}
	dir := strings.Trim(r.URL.Query().Get("path"), "/")
	for p := range s.files[repo] {
		if dir == "" || strings.HasPrefix(p, dir+"/") {
			delete(s.files[repo], p)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteAsset implements DELETE /v1/assets/{id}.
func (s *Server) deleteAsset(w http.ResponseWriter, u user, id string) {
	if u.anonymous {
		s.siesta(w, http.StatusForbidden)
		return
	}
	for repo, files := range s.files {
		for p, f := range files {
			if f.id == id {
				delete(s.files[repo], p)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	s.siesta(w, http.StatusNotFound)
}

// uploadComponent implements POST /v1/components for raw repositories.
func (s *Server) uploadComponent(w http.ResponseWriter, r *http.Request, u user) {
	repo, ok := s.repos[r.URL.Query().Get("repository")]
	if !ok {
		s.siesta(w, http.StatusNotFound)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil { //nolint:gosec // test fake
		textError(w, http.StatusBadRequest, err.Error())
		return
	}
	dir := strings.Trim(r.FormValue("raw.directory"), "/")
	for i := 1; ; i++ {
		fh := r.MultipartForm.File[fmt.Sprintf("raw.asset%d", i)]
		if len(fh) == 0 {
			break
		}
		name := r.FormValue(fmt.Sprintf("raw.asset%d.filename", i))
		path := strings.Trim(dir+"/"+name, "/")
		if !s.mayWrite(w, u, repo, path) {
			return
		}
		f, err := fh[0].Open()
		if err != nil {
			textError(w, http.StatusBadRequest, err.Error())
			return
		}
		content, _ := io.ReadAll(f)
		_ = f.Close()
		s.storeFile(repo.Name, path, content, detectType(path, fh[0].Header.Get("Content-Type")), u.name)
	}
	w.WriteHeader(http.StatusNoContent)
}
