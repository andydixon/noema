// Package version exposes build information injected at link time and
// recorded by the Go toolchain, so replays and experiments can say which
// code produced them.
package version

import (
	"runtime"
	"runtime/debug"
)

// Version is set via -ldflags "-X github.com/andydixon/noema/internal/version.Version=...".
var Version = "dev"

// Build describes the running binary.
type Build struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`     // VCS revision when built from a checkout
	Modified  bool   `json:"modified,omitempty"`   // working tree was dirty at build time
	BuildTime string `json:"build_time,omitempty"` // VCS commit time (reproducible; not the wall clock of the build)
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Info reads the build record. Missing VCS data (a build from a source
// archive rather than a checkout) is left blank, never invented.
func Info() Build {
	b := Build{Version: Version, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if b.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			b.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Commit = s.Value
			case "vcs.modified":
				b.Modified = s.Value == "true"
			case "vcs.time":
				b.BuildTime = s.Value
			}
		}
	}
	return b
}
