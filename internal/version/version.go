// Package version exposes build metadata embedded into the binary at link time.
package version

import (
	"fmt"
	"runtime"
	"time"
)

// Build metadata. These are overridden via -ldflags "-X" at build time
// (see the Makefile); the defaults apply to `go run` and untagged builds.
var (
	version   = "dev"
	gitCommit = "none"
	buildDate = "unknown"
	// expiresAt is an RFC3339 timestamp baked into pre-release (alpha) builds so
	// they stop running after a window, nudging an upgrade. Empty = no expiry
	// (dev and `go run` never expire).
	expiresAt = ""
)

// Info holds build metadata describing how the binary was produced.
type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// Get returns the build metadata embedded at link time, augmented with the
// Go runtime version the binary was compiled with.
func Get() Info {
	return Info{
		Version:   version,
		GitCommit: gitCommit,
		BuildDate: buildDate,
		GoVersion: runtime.Version(),
		ExpiresAt: expiresAt,
	}
}

// ExpiryStatus reports, for the given moment, whether this build carries an
// expiry, when it is, and whether it has passed. A build with no expiry (dev,
// `go run`) reports set=false and never expires.
func ExpiryStatus(now time.Time) (set bool, at time.Time, expired bool) {
	if expiresAt == "" {
		return false, time.Time{}, false
	}
	t, perr := time.Parse(time.RFC3339, expiresAt)
	if perr != nil {
		return false, time.Time{}, false
	}
	return true, t, now.After(t)
}

// String returns a single-line, human-readable rendering of the build metadata.
// A baked-in expiry, when present, is appended so `leoflow version` reveals it.
func (i Info) String() string {
	s := fmt.Sprintf("leoflow %s (commit %s, built %s, %s)",
		i.Version, i.GitCommit, i.BuildDate, i.GoVersion)
	if i.ExpiresAt != "" {
		s += fmt.Sprintf(" [expires %s]", i.ExpiresAt)
	}
	return s
}
