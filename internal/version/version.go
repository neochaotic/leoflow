// Package version exposes build metadata embedded into the binary at link time.
package version

import (
	"fmt"
	"runtime"
)

// Build metadata. These are overridden via -ldflags "-X" at build time
// (see the Makefile); the defaults apply to `go run` and untagged builds.
var (
	version   = "dev"
	gitCommit = "none"
	buildDate = "unknown"
)

// Info holds build metadata describing how the binary was produced.
type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
}

// Get returns the build metadata embedded at link time, augmented with the
// Go runtime version the binary was compiled with.
func Get() Info {
	return Info{
		Version:   version,
		GitCommit: gitCommit,
		BuildDate: buildDate,
		GoVersion: runtime.Version(),
	}
}

// String returns a single-line, human-readable rendering of the build metadata.
func (i Info) String() string {
	return fmt.Sprintf("leoflow %s (commit %s, built %s, %s)",
		i.Version, i.GitCommit, i.BuildDate, i.GoVersion)
}
