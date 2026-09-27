package nexus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
)

// DefaultIdleTimeout is how long a file transfer may go without moving any
// data before it is aborted. Transfers have no overall time limit.
const DefaultIdleTimeout = 5 * time.Minute

// SetIdleTimeout changes the idle timeout of file transfers (tests).
func (c *Client) SetIdleTimeout(d time.Duration) { c.idle = d }

// ContentInfo describes a stored file, taken from the headers of HEAD or GET.
type ContentInfo struct {
	Size         int64 // -1 when unknown
	SHA1         string
	LastModified time.Time // zero when unknown
	ContentType  string
}

// ContentURL returns the URL of a file: <base>/repository/REPO/PATH. Every
// path segment is percent-encoded, including ";", which Jetty would otherwise
// treat as the start of a path parameter.
func (c *Client) ContentURL(repo, path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u := *c.base
	u.RawPath = strings.TrimRight(c.base.EscapedPath(), "/") + "/repository/" + url.PathEscape(repo) + "/" + strings.Join(segs, "/")
	u.Path, _ = url.PathUnescape(u.RawPath)
	u.RawQuery = ""
	return u.String()
}

func contentInfo(resp *http.Response) ContentInfo {
	info := ContentInfo{Size: resp.ContentLength, ContentType: resp.Header.Get("Content-Type")}
	// Nexus sends the SHA-1 of the content as ETag.
	if etag := strings.Trim(strings.TrimPrefix(resp.Header.Get("ETag"), "W/"), `"`); len(etag) == 40 {
		info.SHA1 = strings.ToLower(etag)
	}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		info.LastModified = t.UTC()
	}
	return info
}

// Stat returns information about a file (HEAD). A directory or a missing file
// gives an *APIError with status 404.
func (c *Client) Stat(ctx context.Context, repo, path string) (ContentInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.ContentURL(repo, path), nil)
	if err != nil {
		return ContentInfo{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return ContentInfo{}, err
	}
	_ = resp.Body.Close()
	return contentInfo(resp), nil
}

// Download opens a file (GET). The caller must close the returned body. The
// transfer fails when no data arrives for the idle timeout.
func (c *Client) Download(ctx context.Context, repo, path string) (io.ReadCloser, ContentInfo, error) {
	ctx, w := c.watchIdle(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ContentURL(repo, path), nil)
	if err != nil {
		w.stop()
		return nil, ContentInfo{}, err
	}
	resp, err := c.Do(req) //nolint:bodyclose // the caller closes the returned body
	if err != nil {
		w.stop()
		return nil, ContentInfo{}, w.explain(err)
	}
	return &watchedBody{r: resp.Body, w: w}, contentInfo(resp), nil
}

// UploadBody is the content of an upload.
type UploadBody struct {
	// Open returns the content. It is called again for each retry, so it must
	// return the same content every time, unless Replayable is false.
	Open        func() (io.ReadCloser, error)
	Size        int64 // -1 when unknown
	ContentType string
	Replayable  bool // a failed request may be retried
}

// Upload stores a file with PUT <base>/repository/REPO/PATH.
func (c *Client) Upload(ctx context.Context, repo, path string, body UploadBody) error {
	ctx, w := c.watchIdle(ctx)
	defer w.stop()
	open := func() (io.ReadCloser, error) {
		if body.Size == 0 {
			return http.NoBody, nil
		}
		rc, err := body.Open()
		if err != nil {
			return nil, err
		}
		return &watchedBody{r: rc, w: w, keep: true}, nil
	}
	rc, err := open()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.ContentURL(repo, path), rc)
	if err != nil {
		_ = rc.Close()
		return err
	}
	req.ContentLength = body.Size
	if body.Replayable {
		req.GetBody = open
	}
	req.Header.Set("Content-Type", uploadContentType(body.ContentType))
	resp, err := c.Do(req)
	if err != nil {
		return w.explain(err)
	}
	return resp.Body.Close()
}

// uploadContentType never sends form types: Jetty would take a form body for
// request parameters, and Nexus would store an empty file.
func uploadContentType(ct string) string {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || mt == "application/x-www-form-urlencoded" || strings.HasPrefix(mt, "multipart/") {
		return "application/octet-stream"
	}
	return ct
}

// UploadRawComponent stores one file in a raw repository with the Components
// API (POST /v1/components), in directory dir ("" is the root). The multipart
// body is streamed, not buffered.
func (c *Client) UploadRawComponent(ctx context.Context, repo, dir, name string, body UploadBody) error {
	ctx, w := c.watchIdle(ctx)
	defer w.stop()
	boundary := randomBoundary()
	open := func() (io.ReadCloser, error) {
		src, err := body.Open()
		if err != nil {
			return nil, err
		}
		pr, pw := io.Pipe()
		go func() {
			defer src.Close()
			mw := multipart.NewWriter(pw)
			_ = mw.SetBoundary(boundary)
			err := writeRawParts(mw, dir, name, body.ContentType, &watchedBody{r: src, w: w, keep: true})
			if err == nil {
				err = mw.Close()
			}
			pw.CloseWithError(err)
		}()
		return pr, nil
	}
	rc, err := open()
	if err != nil {
		return err
	}
	target := c.URL("/service/rest/v1/components", url.Values{"repository": {repo}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, rc)
	if err != nil {
		_ = rc.Close()
		return err
	}
	req.ContentLength = -1
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	resp, err := c.Do(req)
	if err != nil {
		return w.explain(err)
	}
	return resp.Body.Close()
}

func writeRawParts(mw *multipart.Writer, dir, name, contentType string, content io.Reader) error {
	if err := mw.WriteField("raw.directory", "/"+dir); err != nil {
		return err
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "raw.asset1", "filename": name}))
	h.Set("Content-Type", uploadContentType(contentType))
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, content); err != nil {
		return err
	}
	return mw.WriteField("raw.asset1.filename", name)
}

func randomBoundary() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "nexr-" + hex.EncodeToString(b[:])
}

// DeleteContent deletes a file with DELETE <base>/repository/REPO/PATH, which
// needs only the delete privilege of the repository. A missing file gives an
// *APIError with status 404.
func (c *Client) DeleteContent(ctx context.Context, repo, path string) error {
	return c.send(ctx, http.MethodDelete, c.ContentURL(repo, path))
}

var errIdle = errors.New("no data was transferred for too long")

// idleWatch cancels a transfer's context when no data moves for a while.
type idleWatch struct {
	d      time.Duration
	timer  *time.Timer
	ctx    context.Context
	cancel context.CancelCauseFunc
}

func (c *Client) watchIdle(parent context.Context) (context.Context, *idleWatch) {
	d := c.idle
	if d <= 0 {
		d = DefaultIdleTimeout
	}
	ctx, cancel := context.WithCancelCause(parent)
	w := &idleWatch{d: d, ctx: ctx, cancel: cancel}
	w.timer = time.AfterFunc(d, func() { cancel(errIdle) })
	return ctx, w
}

func (w *idleWatch) kick() { w.timer.Reset(w.d) }

func (w *idleWatch) stop() {
	w.timer.Stop()
	w.cancel(nil)
}

// explain turns the cancellation by the watchdog into a timeout error.
func (w *idleWatch) explain(err error) error {
	if err != nil && errors.Is(context.Cause(w.ctx), errIdle) {
		return errs.Wrap(errs.KindTimeout, errIdle, "transfer stalled: no data for %s", w.d)
	}
	return err
}

// watchedBody reports progress to the watchdog. Unless keep is set, closing
// it also stops the watchdog.
type watchedBody struct {
	r    io.ReadCloser
	w    *idleWatch
	keep bool
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.w.kick()
	}
	if err != nil && !errors.Is(err, io.EOF) {
		err = b.w.explain(err)
	}
	return n, err
}

func (b *watchedBody) Close() error {
	err := b.r.Close()
	if !b.keep {
		b.w.stop()
	}
	return err
}
