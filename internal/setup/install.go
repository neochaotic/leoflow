package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// maxPythonArchiveBytes caps the relocatable CPython download to guard against a
// runaway or hostile response (the real archive is ~30 MB).
const maxPythonArchiveBytes = 200 << 20 // 200 MiB

// EnsureOpts configures EnsurePython.
type EnsureOpts struct {
	Home     string // the managed root, e.g. ~/.leoflow
	GOOS     string
	GOARCH   string
	Libc     string
	LookPath func(string) (string, error)
	Stat     func(string) (os.FileInfo, error)
	Client   *http.Client
	Logf     func(string, ...any) // optional progress callback
}

// EnsurePython returns the path to a usable Python 3.11: a system interpreter if
// one is on PATH, the managed relocatable CPython if already installed under
// Home, or a freshly downloaded-and-verified pinned build otherwise. The managed
// build is extracted to Home/python (the archive's top-level "python/" dir).
func EnsurePython(ctx context.Context, o EnsureOpts) (string, error) {
	if o.LookPath != nil {
		if p, err := o.LookPath("python3.11"); err == nil {
			return p, nil
		}
	}
	managed := filepath.Join(o.Home, "python", "bin", "python3.11")
	if o.Stat != nil {
		if _, err := o.Stat(managed); err == nil {
			return managed, nil
		}
	}
	build, err := ResolvePython(o.GOOS, o.GOARCH, o.Libc)
	if err != nil {
		return "", err
	}
	logf(o.Logf, "downloading relocatable CPython %s for %s/%s...", build.Version, o.GOOS, o.GOARCH)
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	if derr := downloadVerifyExtract(ctx, client, build, o.Home); derr != nil {
		return "", derr
	}
	logf(o.Logf, "CPython installed at %s", managed)
	return managed, nil
}

// logf invokes the optional progress callback, ignoring a nil one.
func logf(f func(string, ...any), format string, a ...any) {
	if f != nil {
		f(format, a...)
	}
}

// downloadVerifyExtract fetches the build's URL, verifies its SHA-256 against the
// pinned value, and extracts the gzipped tarball into destDir. Extraction is
// guarded against path traversal (zip-slip). The download is verified in full
// before any file is written.
func downloadVerifyExtract(ctx context.Context, client *http.Client, b PythonBuild, destDir string) error {
	data, err := fetchVerify(ctx, client, b.URL, b.SHA256, maxPythonArchiveBytes, "CPython")
	if err != nil {
		return err
	}
	return extractTarGz(data, destDir)
}

// fetchVerify downloads url, caps the body at maxBytes, and verifies its SHA-256
// against wantSHA before returning the bytes — so a managed toolchain (CPython,
// PostgreSQL) is never extracted unverified. label names the artifact in errors.
func fetchVerify(ctx context.Context, client *http.Client, url, wantSHA string, maxBytes int64, label string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", label, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close of the download body
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: unexpected status %s", label, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s archive: %w", label, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s archive exceeds %d bytes", label, maxBytes)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return nil, fmt.Errorf("%s checksum mismatch: got %s, want %s", label, got, wantSHA)
	}
	return data, nil
}

// extractTarGz unpacks a gzipped tar archive into destDir, rejecting any entry
// that would escape destDir.
func extractTarGz(data []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("opening gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }() //nolint:errcheck // best-effort close of the gzip reader

	clean := filepath.Clean(destDir)
	tr := tar.NewReader(gz)
	for {
		hdr, terr := tr.Next()
		if terr == io.EOF {
			return nil
		}
		if terr != nil {
			return fmt.Errorf("reading tar entry: %w", terr)
		}
		target := filepath.Join(clean, hdr.Name) //nolint:gosec // containment enforced in extractEntry
		if err := extractEntry(tr, hdr, clean, target); err != nil {
			return err
		}
	}
}

// extractEntry writes a single tar entry (directory, regular file, or symlink)
// to target. It enforces, at the sink, that target stays within destDir (Zip
// Slip), and that a symlink's resolved target does too — so a malicious archive
// can neither write outside the extraction root nor plant an escaping symlink
// that a later entry could write through.
func extractEntry(tr *tar.Reader, hdr *tar.Header, destDir, target string) error {
	if target != destDir && !strings.HasPrefix(target, destDir+string(os.PathSeparator)) {
		return fmt.Errorf("tar entry %q escapes destination", hdr.Name)
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o750)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("creating parent of %q: %w", target, err)
		}
		// Preserve only the executable bit (interpreters, scripts); everything
		// else is owner read/write. This sidesteps trusting arbitrary archive
		// mode bits while keeping the CPython binaries runnable.
		perm := os.FileMode(0o600)
		if hdr.Mode&0o111 != 0 {
			perm = 0o700
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			return fmt.Errorf("creating %q: %w", target, err)
		}
		if _, err := io.Copy(f, io.LimitReader(tr, maxPythonArchiveBytes)); err != nil { //nolint:gosec // bounded by the archive cap
			_ = f.Close() //nolint:errcheck // already returning an error
			return fmt.Errorf("writing %q: %w", target, err)
		}
		return f.Close()
	case tar.TypeSymlink:
		if err := validateSymlinkTarget(destDir, target, hdr.Linkname); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("creating parent of %q: %w", target, err)
		}
		return os.Symlink(hdr.Linkname, target) //nolint:gosec // link target validated to stay within destDir
	default:
		// Skip device nodes, fifos, and other special types the CPython archive
		// does not use.
		return nil
	}
}

// validateSymlinkTarget rejects a symlink whose target would resolve outside
// destDir: an absolute link, or a relative one that climbs out via "..". This
// stops a hostile archive from planting a symlink that escapes the extraction
// root (and one a later entry could then write through to escape it).
func validateSymlinkTarget(destDir, linkPath, linkname string) error {
	if filepath.IsAbs(linkname) {
		return fmt.Errorf("symlink %q has absolute target %q", linkPath, linkname)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(linkPath), linkname))
	if resolved != destDir && !strings.HasPrefix(resolved, destDir+string(os.PathSeparator)) {
		return fmt.Errorf("symlink %q target %q escapes destination", linkPath, linkname)
	}
	return nil
}
