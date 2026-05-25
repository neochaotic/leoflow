// Package setup implements host detection and bootstrap for `leoflow setup`
// and `leoflow doctor`: it determines the platform, which dependencies are
// present, and which operating tier is achievable, preferring relocatable
// downloads into ~/.leoflow over system package managers.
package setup

import (
	"io/fs"
	"strings"
)

// Tier is an operating mode, ordered by the infrastructure it needs.
type Tier int

const (
	// TierSubprocess runs the agent directly on the host with no isolation — the
	// dev-only escape hatch (ADR 0015). Fastest loop; no Docker or Kubernetes.
	TierSubprocess Tier = iota
	// TierK8s runs each task in an ephemeral pod via client-go, on a local
	// single-node cluster (k3d) or a real cluster. The sole container path
	// (ADR 0015); Docker is only the engine that hosts the local cluster, never
	// an executor itself.
	TierK8s
)

// String returns the tier's short name.
func (t Tier) String() string {
	switch t {
	case TierSubprocess:
		return "subprocess"
	case TierK8s:
		return "k8s"
	default:
		return "unknown"
	}
}

// Probe carries the host facts Detect needs. The function fields are injected so
// detection is testable without touching the real filesystem or PATH.
type Probe struct {
	GOOS     string
	GOARCH   string
	LookPath func(string) (string, error) // os/exec.LookPath
	Stat     func(string) (fs.FileInfo, error)
	Getwd    func() (string, error)
}

// Report is the outcome of Detect: the platform, which tools are present, and
// the highest achievable tier.
type Report struct {
	OS         string
	Arch       string
	Libc       string // "glibc" or "musl" on linux; empty on darwin
	Python311  bool
	PythonPath string
	Docker     bool
	K3d        bool
	Kubectl    bool
	UnderMnt   bool // cwd under /mnt (WSL 9p mount): inotify hot-reload is unreliable
	Tier       Tier
}

// muslLoaders are the dynamic-loader paths that mark a musl-based distro (Alpine).
var muslLoaders = []string{
	"/lib/ld-musl-x86_64.so.1",
	"/lib/ld-musl-aarch64.so.1",
	"/lib/ld-musl-armhf.so.1",
}

// Detect inspects the host and reports the achievable tier. Docker presence
// makes the Kubernetes tier achievable (k3d and kubectl are fetched on demand);
// without Docker the host falls back to the subprocess tier.
func Detect(p Probe) Report {
	has := func(name string) bool {
		if p.LookPath == nil {
			return false
		}
		_, err := p.LookPath(name)
		return err == nil
	}
	r := Report{
		OS:        p.GOOS,
		Arch:      p.GOARCH,
		Libc:      detectLibc(p),
		Docker:    has("docker"),
		K3d:       has("k3d"),
		Kubectl:   has("kubectl"),
		Python311: has("python3.11"),
	}
	if r.Python311 && p.LookPath != nil {
		if path, err := p.LookPath("python3.11"); err == nil {
			r.PythonPath = path
		}
	}
	if p.Getwd != nil {
		if wd, err := p.Getwd(); err == nil && strings.HasPrefix(wd, "/mnt/") {
			r.UnderMnt = true
		}
	}
	if r.Docker {
		r.Tier = TierK8s
	} else {
		r.Tier = TierSubprocess
	}
	return r
}

// detectLibc returns "musl" or "glibc" on linux (based on the dynamic loader)
// and an empty string on other platforms.
func detectLibc(p Probe) string {
	if p.GOOS != "linux" {
		return ""
	}
	if p.Stat != nil {
		for _, loader := range muslLoaders {
			if _, err := p.Stat(loader); err == nil {
				return "musl"
			}
		}
	}
	return "glibc"
}
