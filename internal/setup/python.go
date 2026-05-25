package setup

import "fmt"

// Pinned relocatable CPython build (python-build-standalone). Bumping the
// interpreter means bumping pyReleaseTag + pyVersion and refreshing every
// SHA-256 below from the release's SHA256SUMS file.
const (
	pyReleaseTag = "20260510"
	pyVersion    = "3.11.15"
	pyBaseURL    = "https://github.com/astral-sh/python-build-standalone/releases/download"
)

// PythonBuild is a relocatable CPython release asset and its expected checksum.
type PythonBuild struct {
	URL     string
	SHA256  string
	Version string
}

// pythonTriple maps a (GOOS, GOARCH, libc) host to the python-build-standalone
// target triple. libc is honored only on linux; "" defaults to glibc.
func pythonTriple(goos, goarch, libc string) (string, error) {
	switch goos {
	case "darwin":
		switch goarch {
		case "arm64":
			return "aarch64-apple-darwin", nil
		case "amd64":
			return "x86_64-apple-darwin", nil
		}
	case "linux":
		suffix := "gnu"
		if libc == "musl" {
			suffix = "musl"
		}
		switch goarch {
		case "amd64":
			return "x86_64-unknown-linux-" + suffix, nil
		case "arm64":
			return "aarch64-unknown-linux-" + suffix, nil
		}
	}
	return "", fmt.Errorf("no relocatable CPython build for %s/%s (libc %q)", goos, goarch, libc)
}

// pythonSHA256 holds the pinned checksum for each supported triple.
var pythonSHA256 = map[string]string{
	"aarch64-apple-darwin":       "03bcedae9b19a48888d7dc8ba064f73f6efaaf2b13f6a8e1a1bcc062df13e855",
	"x86_64-apple-darwin":        "5e388e3db8b59c8487ddd1423330b90fc7f0c6ef7eadec945441a180d0dd4bc4",
	"x86_64-unknown-linux-gnu":   "14b5843a3492925dab6fdb7cca7d09af83ddf1fe2851f72cf9b1edc8ed2b1db7",
	"aarch64-unknown-linux-gnu":  "0bc1b7acbb888881addf3a1c887a47d510d4300db6e3ad2ba461154b982e456a",
	"x86_64-unknown-linux-musl":  "2135e9d2f1dea4ba21cf7ec441aa9c7b17fd551819dc8175598efa0fef5012ff",
	"aarch64-unknown-linux-musl": "185a6eab2578bd233c11bb1063374be447f559946444628fba2a519639aa5b08",
}

// ResolvePython returns the pinned relocatable CPython asset for the host, or an
// error if the platform is unsupported (e.g. Windows, where WSL2 is the path).
func ResolvePython(goos, goarch, libc string) (PythonBuild, error) {
	triple, err := pythonTriple(goos, goarch, libc)
	if err != nil {
		return PythonBuild{}, err
	}
	sha, ok := pythonSHA256[triple]
	if !ok {
		return PythonBuild{}, fmt.Errorf("no pinned checksum for triple %q", triple)
	}
	asset := fmt.Sprintf("cpython-%s+%s-%s-install_only.tar.gz", pyVersion, pyReleaseTag, triple)
	return PythonBuild{
		URL:     fmt.Sprintf("%s/%s/%s", pyBaseURL, pyReleaseTag, asset),
		SHA256:  sha,
		Version: pyVersion,
	}, nil
}
