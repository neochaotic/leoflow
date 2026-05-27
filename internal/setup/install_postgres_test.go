package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarGz builds a gzipped tar from the given headers (Size/body taken from each
// header's paired body, "" for non-regular entries) for extraction tests.
func tarGz(t *testing.T, entries []*tar.Header, bodies []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for i, h := range entries {
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(bodies[i]))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(bodies[i])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestExtractTarGzStripRejectsZipSlip proves an archive entry that climbs out of
// the destination via ".." (even after the top-level strip) is refused and never
// written.
func TestExtractTarGzStripRejectsZipSlip(t *testing.T) {
	data := tarGz(t,
		[]*tar.Header{{Name: "../../evil.txt", Typeflag: tar.TypeReg, Mode: 0o644}},
		[]string{"pwned"})
	dest := t.TempDir()
	err := extractTarGzStrip(data, dest, 1)
	if err == nil || !strings.Contains(err.Error(), "escapes destination") {
		t.Fatalf("err = %v, want an 'escapes destination' rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); !os.IsNotExist(statErr) {
		t.Error("zip-slip entry was written outside the destination")
	}
}

// TestExtractTarGzStripRejectsEscapingSymlink proves a symlink whose target
// resolves outside the destination is refused (so a later entry cannot be
// written through it to escape the extraction root).
func TestExtractTarGzStripRejectsEscapingSymlink(t *testing.T) {
	data := tarGz(t,
		[]*tar.Header{{Name: "pg/link", Linkname: "../../../../etc/passwd", Typeflag: tar.TypeSymlink, Mode: 0o777}},
		[]string{""})
	dest := t.TempDir()
	err := extractTarGzStrip(data, dest, 1)
	if err == nil || !strings.Contains(err.Error(), "escapes destination") {
		t.Fatalf("err = %v, want an 'escapes destination' rejection", err)
	}
}

// TestExtractTarGzStripAllowsInternalSymlink confirms a symlink whose target
// stays within the destination (the common lib/*.so.N → *.so case) is allowed.
func TestExtractTarGzStripAllowsInternalSymlink(t *testing.T) {
	data := tarGz(t,
		[]*tar.Header{
			{Name: "pg/lib/libpq.so.5", Typeflag: tar.TypeReg, Mode: 0o755},
			{Name: "pg/lib/libpq.so", Linkname: "libpq.so.5", Typeflag: tar.TypeSymlink, Mode: 0o777},
		},
		[]string{"SO", ""})
	dest := t.TempDir()
	if err := extractTarGzStrip(data, dest, 1); err != nil {
		t.Fatalf("extractTarGzStrip rejected a valid internal symlink: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "lib", "libpq.so")); err != nil {
		t.Errorf("internal symlink was not created: %v", err)
	}
}

// errRoundTripper fails any HTTP request, proving a code path made no network call.
type errRoundTripper struct{ t *testing.T }

func (e errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	e.t.Error("unexpected network call")
	return nil, errors.New("no network")
}

func TestEnsurePostgresReturnsExistingManaged(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, "postgres", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "postgres"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A managed Postgres already present must short-circuit before any download.
	got, err := EnsurePostgres(context.Background(), EnsureOpts{
		Home: home, GOOS: "linux", GOARCH: "arm64",
		Client: &http.Client{Transport: errRoundTripper{t}},
	})
	if err != nil {
		t.Fatalf("EnsurePostgres: %v", err)
	}
	if got != binDir {
		t.Errorf("bin dir = %q, want %q", got, binDir)
	}
}

func TestStripComponents(t *testing.T) {
	cases := map[string]string{
		"postgresql-16.13.0-x/bin/postgres": filepath.Join("bin", "postgres"),
		"postgresql-16.13.0-x/lib/a.so":     filepath.Join("lib", "a.so"),
		"top/":                              "", // the stripped top-level dir itself
		"top":                               "",
	}
	for in, want := range cases {
		if got := stripComponents(in, 1); got != want {
			t.Errorf("stripComponents(%q, 1) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractTarGzStripFlattensTopDir(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "pg-16/", Typeflag: tar.TypeDir, Mode: 0o755})
	for name, body := range map[string]string{"pg-16/bin/postgres": "BIN", "pg-16/share/x.sql": "SQL"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()

	dest := t.TempDir()
	if err := extractTarGzStrip(buf.Bytes(), dest, 1); err != nil {
		t.Fatalf("extractTarGzStrip: %v", err)
	}
	// The top-level pg-16/ is stripped: bin/postgres lands directly under dest.
	if b, err := os.ReadFile(filepath.Join(dest, "bin", "postgres")); err != nil || string(b) != "BIN" {
		t.Errorf("bin/postgres = %q (err %v), want BIN", b, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "pg-16")); !os.IsNotExist(err) {
		t.Error("the top-level archive dir must be stripped, not present under dest")
	}
}
