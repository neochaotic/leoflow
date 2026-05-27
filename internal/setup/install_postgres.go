package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// maxPostgresArchiveBytes caps the relocatable PostgreSQL download. The real
// archive is ~11 MB; the bound guards against a runaway/hostile response.
const maxPostgresArchiveBytes = 150 << 20 // 150 MiB

// EnsurePostgres returns the bin directory of a managed relocatable PostgreSQL
// (Home/postgres/bin), downloading + verifying + extracting the pinned build if
// it is not already present. Unlike Python there is NO system fallback: Lite
// manages its own Postgres so the version and on-disk layout are predictable and
// — the point of Fase 2 — it needs no Docker. The data directory and process
// lifecycle are the caller's concern (see the Lite runner).
func EnsurePostgres(ctx context.Context, o EnsureOpts) (string, error) {
	binDir := filepath.Join(o.Home, "postgres", "bin")
	managed := filepath.Join(binDir, "postgres")
	stat := o.Stat
	if stat == nil {
		stat = os.Stat
	}
	if _, err := stat(managed); err == nil {
		return binDir, nil
	}
	build, err := ResolvePostgres(o.GOOS, o.GOARCH, o.Libc)
	if err != nil {
		return "", err
	}
	logf(o.Logf, "downloading relocatable PostgreSQL %s for %s/%s...", build.Version, o.GOOS, o.GOARCH)
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	data, err := fetchVerify(ctx, client, build.URL, build.SHA256, maxPostgresArchiveBytes, "PostgreSQL")
	if err != nil {
		return "", err
	}
	// theseus-rs nests everything under a single postgresql-<version>-<triple>/
	// directory; strip it so bin/postgres lands at Home/postgres/bin/postgres
	// regardless of the triple.
	if err := extractTarGzStrip(data, filepath.Join(o.Home, "postgres"), 1); err != nil {
		return "", err
	}
	logf(o.Logf, "PostgreSQL installed at %s", binDir)
	return binDir, nil
}

// extractTarGzStrip unpacks a gzipped tar into destDir, dropping the first strip
// leading path components from each entry (so a single top-level archive dir is
// flattened away), and rejecting any entry that would escape destDir.
func extractTarGzStrip(data []byte, destDir string, strip int) error {
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
		name := stripComponents(hdr.Name, strip)
		if name == "" {
			continue // the stripped top-level dir itself
		}
		target := filepath.Join(clean, name) //nolint:gosec // containment enforced in extractEntry
		if err := extractEntry(tr, hdr, clean, target); err != nil {
			return err
		}
	}
}

// stripComponents drops the first n leading path components of a slash- or
// OS-separated archive name, returning "" when nothing remains.
func stripComponents(name string, n int) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(name)), "/")
	if len(parts) <= n {
		return ""
	}
	return filepath.Join(parts[n:]...)
}
