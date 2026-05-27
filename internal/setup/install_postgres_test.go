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
	"testing"
)

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
