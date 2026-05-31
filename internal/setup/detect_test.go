package setup

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// presentPaths builds a LookPath that succeeds only for the named executables.
func presentPaths(names ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func statNone() func(string) (fs.FileInfo, error) {
	return func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist }
}

// statPresent reports existence only for the given paths.
func statPresent(paths ...string) func(string) (fs.FileInfo, error) {
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	return func(p string) (fs.FileInfo, error) {
		if set[p] {
			return nil, nil
		}
		return nil, os.ErrNotExist
	}
}

func TestDetect(t *testing.T) {
	t.Run("docker present makes k8s achievable", func(t *testing.T) {
		r := Detect(Probe{
			GOOS: "linux", GOARCH: "amd64",
			LookPath: presentPaths("docker", "python3.11"),
			Stat:     statNone(),
			Getwd:    func() (string, error) { return "/home/u/proj", nil },
		})
		if r.Tier != TierK8s {
			t.Errorf("Tier = %v, want TierK8s (docker present)", r.Tier)
		}
		if !r.Docker || !r.PythonAvailable {
			t.Errorf("Docker=%v PythonAvailable=%v, want both true", r.Docker, r.PythonAvailable)
		}
		if r.Libc != "glibc" {
			t.Errorf("Libc = %q, want glibc (no musl loader)", r.Libc)
		}
	})

	// TestDetectAcceptsNewerPythonOnPath covers #D4: the detector previously
	// only recognized `python3.11` and forced a ~50 MB CPython download for
	// users who already had a newer interpreter installed. It now accepts any
	// `python3.11` / `python3.12` / `python3.13` on PATH (we trust the binary
	// name convention; the parser shim is stdlib-only per ADR 0024 so any
	// 3.11+ works).
	t.Run("python3.12 alone is enough — no forced download", func(t *testing.T) {
		r := Detect(Probe{
			GOOS: "darwin", GOARCH: "arm64",
			LookPath: presentPaths("python3.12"),
			Stat:     statNone(),
			Getwd:    func() (string, error) { return "/Users/u/proj", nil },
		})
		if !r.PythonAvailable {
			t.Errorf("PythonAvailable=false, want true (python3.12 is on PATH)")
		}
		if !strings.HasSuffix(r.PythonPath, "python3.12") {
			t.Errorf("PythonPath = %q, want a python3.12 path", r.PythonPath)
		}
	})

	t.Run("python3.13 alone is enough", func(t *testing.T) {
		r := Detect(Probe{
			GOOS: "darwin", GOARCH: "arm64",
			LookPath: presentPaths("python3.13"),
			Stat:     statNone(),
			Getwd:    func() (string, error) { return "/Users/u/proj", nil },
		})
		if !r.PythonAvailable || !strings.HasSuffix(r.PythonPath, "python3.13") {
			t.Errorf("PythonAvailable=%v PythonPath=%q, want true/python3.13", r.PythonAvailable, r.PythonPath)
		}
	})

	t.Run("python3.11 still wins when present alongside newer ones", func(t *testing.T) {
		r := Detect(Probe{
			GOOS: "darwin", GOARCH: "arm64",
			LookPath: presentPaths("python3.11", "python3.12", "python3.13"),
			Stat:     statNone(),
			Getwd:    func() (string, error) { return "/Users/u/proj", nil },
		})
		if !strings.HasSuffix(r.PythonPath, "python3.11") {
			t.Errorf("PythonPath = %q, want python3.11 (preferred when present)", r.PythonPath)
		}
	})

	t.Run("no docker falls back to subprocess", func(t *testing.T) {
		r := Detect(Probe{
			GOOS: "linux", GOARCH: "amd64",
			LookPath: presentPaths("python3.11"),
			Stat:     statNone(),
			Getwd:    func() (string, error) { return "/home/u/proj", nil },
		})
		if r.Tier != TierSubprocess {
			t.Errorf("Tier = %v, want TierSubprocess", r.Tier)
		}
		if r.Docker {
			t.Error("Docker = true, want false")
		}
	})

	t.Run("musl loader is detected", func(t *testing.T) {
		r := Detect(Probe{
			GOOS: "linux", GOARCH: "arm64",
			LookPath: presentPaths(),
			Stat:     statPresent("/lib/ld-musl-aarch64.so.1"),
			Getwd:    func() (string, error) { return "/root", nil },
		})
		if r.Libc != "musl" {
			t.Errorf("Libc = %q, want musl", r.Libc)
		}
	})

	t.Run("darwin has no libc and starts at subprocess", func(t *testing.T) {
		r := Detect(Probe{
			GOOS: "darwin", GOARCH: "arm64",
			LookPath: presentPaths("python3.11"),
			Stat:     statNone(),
			Getwd:    func() (string, error) { return "/Users/u/proj", nil },
		})
		if r.Libc != "" {
			t.Errorf("Libc = %q, want empty on darwin", r.Libc)
		}
		if r.OS != "darwin" || r.Arch != "arm64" {
			t.Errorf("OS/Arch = %q/%q, want darwin/arm64", r.OS, r.Arch)
		}
	})

	t.Run("cwd under /mnt flags the WSL hot-reload caveat", func(t *testing.T) {
		r := Detect(Probe{
			GOOS: "linux", GOARCH: "amd64",
			LookPath: presentPaths("docker"),
			Stat:     statNone(),
			Getwd:    func() (string, error) { return "/mnt/c/Users/u/proj", nil },
		})
		if !r.UnderMnt {
			t.Error("UnderMnt = false, want true for /mnt/c path")
		}
	})

	t.Run("k3d and kubectl presence is reported", func(t *testing.T) {
		r := Detect(Probe{
			GOOS: "linux", GOARCH: "amd64",
			LookPath: presentPaths("docker", "k3d", "kubectl"),
			Stat:     statNone(),
			Getwd:    func() (string, error) { return "/home/u", nil },
		})
		if !r.K3d || !r.Kubectl {
			t.Errorf("K3d=%v Kubectl=%v, want both true", r.K3d, r.Kubectl)
		}
	})
}

func TestTierString(t *testing.T) {
	cases := map[Tier]string{
		TierSubprocess: "subprocess",
		TierK8s:        "k8s",
		Tier(99):       "unknown",
	}
	for tier, want := range cases {
		if got := tier.String(); got != want {
			t.Errorf("Tier(%d).String() = %q, want %q", int(tier), got, want)
		}
	}
}
