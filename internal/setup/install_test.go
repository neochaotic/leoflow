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

// makeTarGz builds an in-memory .tar.gz from name->content entries and returns
// the bytes plus their hex SHA-256.
func makeTarGz(t *testing.T, entries map[string]string) (archive []byte, sha string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func serve(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadVerifyExtract(t *testing.T) {
	t.Run("good checksum extracts files", func(t *testing.T) {
		body, sum := makeTarGz(t, map[string]string{
			"python/bin/python3.11": "#!/fake interpreter",
			"python/lib/marker":     "x",
		})
		srv := serve(t, body)
		dest := t.TempDir()

		err := downloadVerifyExtract(context.Background(), srv.Client(),
			PythonBuild{URL: srv.URL, SHA256: sum}, dest)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		got, rerr := os.ReadFile(filepath.Join(dest, "python", "bin", "python3.11"))
		if rerr != nil {
			t.Fatalf("reading extracted file: %v", rerr)
		}
		if string(got) != "#!/fake interpreter" {
			t.Errorf("content = %q, want the interpreter stub", got)
		}
	})

	t.Run("checksum mismatch is rejected", func(t *testing.T) {
		body, _ := makeTarGz(t, map[string]string{"python/x": "y"})
		srv := serve(t, body)
		dest := t.TempDir()

		err := downloadVerifyExtract(context.Background(), srv.Client(),
			PythonBuild{URL: srv.URL, SHA256: "deadbeef"}, dest)
		if err == nil {
			t.Fatal("err = nil, want checksum mismatch error")
		}
	})

	t.Run("path traversal entry is rejected", func(t *testing.T) {
		body, sum := makeTarGz(t, map[string]string{"../escape": "evil"})
		srv := serve(t, body)
		dest := t.TempDir()

		err := downloadVerifyExtract(context.Background(), srv.Client(),
			PythonBuild{URL: srv.URL, SHA256: sum}, dest)
		if err == nil {
			t.Fatal("err = nil, want path-traversal rejection")
		}
	})
}

func TestEnsurePythonBranches(t *testing.T) {
	t.Run("system python3.11 on PATH is used", func(t *testing.T) {
		got, err := EnsurePython(context.Background(), EnsureOpts{
			Home:     t.TempDir(),
			GOOS:     "linux",
			GOARCH:   "amd64",
			LookPath: func(string) (string, error) { return "/usr/bin/python3.11", nil },
			Stat:     func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "/usr/bin/python3.11" {
			t.Errorf("path = %q, want the system interpreter", got)
		}
	})

	t.Run("managed python is reused when present", func(t *testing.T) {
		home := t.TempDir()
		managed := filepath.Join(home, "python", "bin", "python3.11")
		if err := os.MkdirAll(filepath.Dir(managed), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(managed, []byte("#!/fake"), 0o700); err != nil {
			t.Fatal(err)
		}
		got, err := EnsurePython(context.Background(), EnsureOpts{
			Home:     home,
			GOOS:     "linux",
			GOARCH:   "amd64",
			LookPath: func(string) (string, error) { return "", os.ErrNotExist },
			Stat:     os.Stat,
		})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != managed {
			t.Errorf("path = %q, want managed %q", got, managed)
		}
	})

	t.Run("unsupported platform errors before any download", func(t *testing.T) {
		_, err := EnsurePython(context.Background(), EnsureOpts{
			Home:     t.TempDir(),
			GOOS:     "windows",
			GOARCH:   "amd64",
			LookPath: func(string) (string, error) { return "", os.ErrNotExist },
			Stat:     func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		})
		if err == nil {
			t.Fatal("err = nil, want unsupported-platform error")
		}
	})
}
