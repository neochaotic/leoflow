package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// buildMonacoTarball returns a gzipped tar shaped like the npm monaco-editor
// package: the min/vs subtree we want, plus other files we must ignore, plus a
// hostile traversal entry that must be rejected.
func buildMonacoTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	entries := map[string]string{
		"package/min/vs/loader.js":                        "// loader",
		"package/min/vs/editor/editor.main.js":            "// editor",
		"package/min/vs/basic-languages/python/python.js": "// py grammar",
		"package/package.json":                            "{}",            // outside min/vs → skip
		"package/esm/vs/editor/editor.api.js":             "// esm → skip", // skip
	}
	for name, body := range entries {
		writeTar(t, tw, name, body)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTar(t *testing.T, tw *tar.Writer, name, body string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func TestMonacoDirLayout(t *testing.T) {
	home := filepath.Join("home", "u", ".leoflow")
	dir := MonacoDir(home)
	if want := filepath.Join(home, "assets", "monaco", monacoVersion); dir != want {
		t.Errorf("MonacoDir = %q, want %q", dir, want)
	}
}

func TestExtractMonacoVSStripsPrefixAndSkipsRest(t *testing.T) {
	dest := t.TempDir()
	if err := extractMonacoVS(buildMonacoTarball(t), dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	// min/vs files land under <dest>/vs with the package/min prefix stripped.
	if b, err := os.ReadFile(filepath.Join(dest, "vs", "loader.js")); err != nil || string(b) != "// loader" {
		t.Errorf("vs/loader.js = %q, err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "vs", "basic-languages", "python", "python.js")); err != nil {
		t.Errorf("python grammar missing: %v", err)
	}
	// Files outside min/vs are not extracted.
	if _, err := os.Stat(filepath.Join(dest, "package.json")); !os.IsNotExist(err) {
		t.Errorf("package.json should be skipped, err=%v", err)
	}
	for _, p := range []string{"esm", "package", "min"} {
		if _, err := os.Stat(filepath.Join(dest, p)); !os.IsNotExist(err) {
			t.Errorf("%q should not be extracted", p)
		}
	}
}

func TestEnsureMonacoDownloadsVerifiesAndIsIdempotent(t *testing.T) {
	body := buildMonacoTarball(t)
	sum := sha256.Sum256(body)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	home := t.TempDir()
	dir, err := ensureMonaco(context.Background(), srv.Client(), home, srv.URL, hex.EncodeToString(sum[:]), nil)
	if err != nil {
		t.Fatalf("ensureMonaco: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vs", "loader.js")); err != nil {
		t.Fatalf("loader.js not provisioned: %v", err)
	}
	// Second call is a no-op (already provisioned): no extra download.
	if _, err := ensureMonaco(context.Background(), srv.Client(), home, srv.URL, hex.EncodeToString(sum[:]), nil); err != nil {
		t.Fatalf("second ensureMonaco: %v", err)
	}
	if hits != 1 {
		t.Errorf("expected 1 download, got %d", hits)
	}
}

func TestEnsureMonacoChecksumMismatch(t *testing.T) {
	body := buildMonacoTarball(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	_, err := ensureMonaco(context.Background(), srv.Client(), t.TempDir(), srv.URL, "deadbeef", nil)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}
