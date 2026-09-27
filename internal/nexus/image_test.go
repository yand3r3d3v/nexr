package nexus

import (
	"encoding/json"
	"testing"
)

func TestByteSize(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int64
	}{
		{`121`, 121},
		{`2202010`, 2202010},
		{`12.0`, 12},
		{`"121 B"`, 121},
		{`"4.75 KB"`, 4864},
		{`"2.10 MB"`, 2202010},
		{`"1 GB"`, 1 << 30},
		{`"77"`, 77},
		{`"many"`, -1},
		{`"3 XB"`, -1},
		{`"-1 MB"`, -1},
		{`true`, -1},
	} {
		var b byteSize
		if err := json.Unmarshal([]byte(tt.in), &b); err != nil || int64(b) != tt.want {
			t.Errorf("%s: %d, %v; want %d", tt.in, b, err, tt.want)
		}
	}
}

func TestImageInfo(t *testing.T) {
	var a assetJSON
	doc := `{"format": "docker", "checksum": {"sha256": "abc"}, "docker": {"totalSize": "2.10 MB", "os": "linux"}}`
	if err := json.Unmarshal([]byte(doc), &a); err != nil {
		t.Fatal(err)
	}
	if img := a.asset().Image; img == nil || img.Digest != "sha256:abc" || img.TotalSize != 2202010 || img.OS != "linux" {
		t.Fatalf("image %+v", img)
	}
	a = assetJSON{Format: "raw"}
	if a.asset().Image != nil {
		t.Fatal("raw assets have no image attributes")
	}
}
