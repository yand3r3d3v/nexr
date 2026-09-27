// Package buildinfo exposes version information embedded at build time.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// These variables are set with -ldflags "-X github.com/yand3r3d3v/nexr/internal/buildinfo.Version=...".
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Info describes the running binary.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Get returns the build information. When the binary was built without
// -ldflags (for example with "go install"), commit and date are taken from the
// VCS stamp that the Go toolchain embeds.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = s.Value
				}
			}
		}
	}
	return info
}

// UserAgent returns the User-Agent header value sent with every request.
func UserAgent() string {
	return fmt.Sprintf("nexr/%s (%s/%s)", Get().Version, runtime.GOOS, runtime.GOARCH)
}
