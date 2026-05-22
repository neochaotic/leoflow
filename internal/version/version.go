// Package version exposes build metadata embedded into the binary at link time.
package version

// Info holds build metadata describing how the binary was produced.
type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
}

// Get returns the build metadata embedded at link time.
func Get() Info { return Info{} }

// String returns a single-line, human-readable rendering of the build metadata.
func (i Info) String() string { return "" }
