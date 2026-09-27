package nexus

import (
	"bytes"
	"context"
	"encoding/json"
	"iter"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Asset is a stored file. Its path never starts with a slash, whatever the
// server sends (raw asset paths start with one since the SQL releases).
type Asset struct {
	ID             string
	Repository     string
	Format         string
	Path           string
	DownloadURL    string
	ContentType    string
	Size           int64
	Checksum       Checksums
	LastModified   time.Time // zero when unknown
	BlobCreated    time.Time // zero when unknown
	LastDownloaded time.Time // zero when never downloaded
	Uploader       string
	// Image holds the attributes of docker and oci manifests; nil for other
	// assets.
	Image *ImageInfo
}

// ImageInfo holds what Nexus records about an image manifest. Only the digest
// is always known: the other attributes exist on recent releases (3.96, not
// 3.71) and only for the docker format. For an index they describe just one of
// its platforms.
type ImageInfo struct {
	Digest       string    // "sha256:…"; empty when unknown
	Created      time.Time // build time from the image config; zero when unknown or unset
	OS           string
	Architecture string
	TotalSize    int64 // bytes of the config and layers; -1 when unknown
}

// Checksums of an asset, as hex strings. Empty values are unknown.
type Checksums struct {
	SHA1   string `json:"sha1,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	SHA512 string `json:"sha512,omitempty"`
	MD5    string `json:"md5,omitempty"`
}

type assetJSON struct {
	ID             string     `json:"id"`
	Repository     string     `json:"repository"`
	Format         string     `json:"format"`
	Path           string     `json:"path"`
	DownloadURL    string     `json:"downloadUrl"`
	ContentType    string     `json:"contentType"`
	FileSize       int64      `json:"fileSize"`
	Checksum       Checksums  `json:"checksum"`
	LastModified   jsonTime   `json:"lastModified"`
	BlobCreated    jsonTime   `json:"blobCreated"`
	LastDownloaded jsonTime   `json:"lastDownloaded"`
	Uploader       string     `json:"uploader"`
	Docker         *imageJSON `json:"docker"`
	OCI            *imageJSON `json:"oci"`
}

type imageJSON struct {
	ContentDigest string    `json:"content_digest"`
	Created       jsonTime  `json:"created"`
	OS            string    `json:"os"`
	Architecture  string    `json:"architecture"`
	TotalSize     *byteSize `json:"totalSize"`
}

func (a assetJSON) asset() Asset {
	return Asset{
		ID: a.ID, Repository: a.Repository, Format: a.Format,
		Path:        strings.TrimLeft(a.Path, "/"),
		DownloadURL: a.DownloadURL, ContentType: a.ContentType, Size: a.FileSize,
		Checksum:     a.Checksum,
		LastModified: a.LastModified.Time, BlobCreated: a.BlobCreated.Time, LastDownloaded: a.LastDownloaded.Time,
		Uploader: a.Uploader,
		Image:    a.image(),
	}
}

func (a assetJSON) image() *ImageInfo {
	if a.Format != "docker" && a.Format != "oci" {
		return nil
	}
	info := &ImageInfo{TotalSize: -1}
	if a.Checksum.SHA256 != "" {
		info.Digest = "sha256:" + a.Checksum.SHA256
	}
	attrs := a.Docker
	if attrs == nil {
		attrs = a.OCI
	}
	if attrs == nil {
		return info
	}
	if attrs.ContentDigest != "" {
		info.Digest = attrs.ContentDigest
	}
	// Images built without a date (e.g. by crane or ko) report year 1.
	if attrs.Created.Year() > 1 {
		info.Created = attrs.Created.Time
	}
	info.OS, info.Architecture = attrs.OS, attrs.Architecture
	if attrs.TotalSize != nil {
		info.TotalSize = int64(*attrs.TotalSize)
	}
	return info
}

// byteSize is docker.totalSize: a number of bytes in search results, but a
// rounded string such as "2.10 MB" (1024-based units) in GET /v1/assets.
// Unknown values decode to -1.
type byteSize int64

func (b *byteSize) UnmarshalJSON(data []byte) error {
	*b = -1
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		if v, err := n.Int64(); err == nil {
			*b = byteSize(v)
		} else if f, err := n.Float64(); err == nil {
			*b = byteSize(math.Round(f))
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	num, unit, _ := strings.Cut(strings.TrimSpace(s), " ")
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return nil
	}
	exp := 0 // a bare number is bytes
	if unit = strings.ToUpper(strings.TrimSpace(unit)); unit != "" {
		if exp = strings.IndexByte("BKMGTP", unit[0]); exp < 0 {
			return nil
		}
	}
	*b = byteSize(math.Round(f * math.Pow(1024, float64(exp))))
	return nil
}

// jsonTime is a timestamp that decodes leniently: null, an empty string or an
// unknown format become the zero time instead of failing the whole listing.
type jsonTime struct{ time.Time }

func (t *jsonTime) UnmarshalJSON(b []byte) error {
	s := string(bytes.Trim(b, `"`))
	if s == "" || s == "null" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02T15:04:05.999999999"} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v.UTC()
			return nil
		}
	}
	return nil
}

func assets(seq iter.Seq2[assetJSON, error]) iter.Seq2[Asset, error] {
	return func(yield func(Asset, error) bool) {
		for a, err := range seq {
			if !yield(a.asset(), err) || err != nil {
				return
			}
		}
	}
}

// Assets iterates over every asset of a repository (GET /v1/assets). The
// result is always up to date, but the whole repository is read, 100 assets
// per request on 3.96 and 10 on 3.71.
func (c *Client) Assets(ctx context.Context, repo string) iter.Seq2[Asset, error] {
	return assets(Paginate[assetJSON](ctx, c, "/v1/assets", url.Values{"repository": {repo}}))
}

// AssetQuery selects assets for SearchAssets.
type AssetQuery struct {
	Repository string
	// Group selects raw assets by directory: `"/dir"` (quoted) matches the
	// files directly in /dir, "/dir*" every file below /dir, and also below
	// /dir-sibling, so results must be filtered by path.
	Group string
	// Name selects raw assets by path: `"/dir/file"` (quoted) matches one file.
	Name string
}

// SearchAssets iterates over the assets that match q (GET /v1/search/assets).
// Search results can lag a few seconds behind uploads.
func (c *Client) SearchAssets(ctx context.Context, q AssetQuery) iter.Seq2[Asset, error] {
	v := url.Values{"repository": {q.Repository}}
	if q.Group != "" {
		v.Set("group", q.Group)
	}
	if q.Name != "" {
		v.Set("name", q.Name)
	}
	return assets(Paginate[assetJSON](ctx, c, "/v1/search/assets", v))
}

// QuotePath returns an exact search value for a raw directory (group) or file
// (name): `"/dir"`, or `"/"` for the root. Quoted values also work with
// spaces, semicolons and quotes in names.
func QuotePath(dir string) string {
	return `"/` + strings.ReplaceAll(strings.ReplaceAll(dir, `\`, `\\`), `"`, `\"`) + `"`
}

// DeleteAsset deletes an asset by its ID (DELETE /v1/assets/{id}).
func (c *Client) DeleteAsset(ctx context.Context, id string) error {
	return c.send(ctx, http.MethodDelete, c.URL("/service/rest/v1/assets/"+url.PathEscape(id), nil))
}

// send makes a request without a body and discards the response.
func (c *Client) send(ctx context.Context, method, target string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
