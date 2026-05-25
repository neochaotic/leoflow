package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/workspace"
)

// ideServer wires a server whose IDE routes are backed by a real workspace FS
// rooted at dir. A nil fs leaves the IDE disabled.
func ideServer(fs WorkspaceFS) *gin.Engine {
	return ideServerWithMonaco(fs, "")
}

// ideServerWithMonaco is ideServer plus a Monaco assets directory to serve.
func ideServerWithMonaco(fs WorkspaceFS, monacoDir string) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Workspace:     fs,
		MonacoDir:     monacoDir,
	})
}

func newWorkspace(t *testing.T) (store WorkspaceFS, dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), []byte("print('hi')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := workspace.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return fs, dir
}

func TestIDETree(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	rec := authGet(srv, http.MethodGet, "/api/v2/ide/tree", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("tree = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Entries []workspace.Entry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Path != "dag.py" {
		t.Fatalf("unexpected tree: %+v", got.Entries)
	}
}

func TestIDEReadFile(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	rec := authGet(srv, http.MethodGet, "/api/v2/ide/file?path=dag.py", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != "dag.py" || got.Content != "print('hi')\n" {
		t.Fatalf("unexpected read: %+v", got)
	}
}

func TestIDEWriteFile(t *testing.T) {
	fs, dir := newWorkspace(t)
	srv := ideServer(fs)
	rec := authGet(srv, http.MethodPut, "/api/v2/ide/file", `{"path":"dag.py","content":"print('bye')\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("write = %d (%s)", rec.Code, rec.Body.String())
	}
	on, _ := os.ReadFile(filepath.Join(dir, "dag.py"))
	if string(on) != "print('bye')\n" {
		t.Fatalf("file not written, got %q", on)
	}
}

func TestIDECreateAndDelete(t *testing.T) {
	fs, dir := newWorkspace(t)
	srv := ideServer(fs)
	// Create a file.
	rec := authGet(srv, http.MethodPost, "/api/v2/ide/file", `{"path":"tasks/new.py"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "tasks", "new.py")); err != nil {
		t.Fatalf("created file missing: %v", err)
	}
	// Delete it.
	rec = authGet(srv, http.MethodDelete, "/api/v2/ide/file?path=tasks/new.py", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "tasks", "new.py")); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
}

func TestIDETraversalRejected(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	if r := authGet(srv, http.MethodGet, "/api/v2/ide/file?path=../../etc/passwd", ""); r.Code != http.StatusBadRequest {
		t.Errorf("read traversal = %d, want 400", r.Code)
	}
	if r := authGet(srv, http.MethodPut, "/api/v2/ide/file", `{"path":"../escape.py","content":"x"}`); r.Code != http.StatusBadRequest {
		t.Errorf("write traversal = %d, want 400", r.Code)
	}
}

func TestIDEReadMissingIs404(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	if r := authGet(srv, http.MethodGet, "/api/v2/ide/file?path=nope.py", ""); r.Code != http.StatusNotFound {
		t.Errorf("read missing = %d, want 404", r.Code)
	}
}

func TestIDEMissingPathParamIs400(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	if r := authGet(srv, http.MethodGet, "/api/v2/ide/file", ""); r.Code != http.StatusBadRequest {
		t.Errorf("missing path = %d, want 400", r.Code)
	}
}

// TestIDEDisabledWhenNoWorkspace asserts the IDE is gated: with no workspace
// (Production, or Lite without one configured) the routes are not registered.
func TestIDEDisabledWhenNoWorkspace(t *testing.T) {
	srv := ideServer(nil)
	if r := authGet(srv, http.MethodGet, "/api/v2/ide/tree", ""); r.Code != http.StatusNotFound {
		t.Errorf("tree without workspace = %d, want 404 (route absent)", r.Code)
	}
	if r := authGet(srv, http.MethodGet, "/ide", ""); r.Code != http.StatusNotFound {
		t.Errorf("/ide without workspace = %d, want 404 (route absent)", r.Code)
	}
}

func TestIDEPageServed(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	rec := authGet(srv, http.MethodGet, "/ide", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ide = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, marker := range []string{"<html", "monaco", "/api/v2/ide/tree", "Leoflow"} {
		if !strings.Contains(body, marker) {
			t.Errorf("/ide page missing marker %q", marker)
		}
	}
}

func TestIDEMonacoNotProvisioned404(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs) // no Monaco dir
	if r := authGet(srv, http.MethodGet, "/ide/vs/loader.js", ""); r.Code != http.StatusNotFound {
		t.Errorf("monaco loader without dir = %d, want 404", r.Code)
	}
}

func TestIDEMonacoServedFromDir(t *testing.T) {
	fs, _ := newWorkspace(t)
	mdir := t.TempDir()
	// The bundle lives in a vs/ subdir, matching how Monaco is extracted and how
	// the page requests it (/ide/vs/...).
	if err := os.MkdirAll(filepath.Join(mdir, "vs", "editor"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdir, "vs", "loader.js"), []byte("// monaco loader"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdir, "vs", "editor", "editor.main.js"), []byte("// editor"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := ideServerWithMonaco(fs, mdir)
	if rec := authGet(srv, http.MethodGet, "/ide/vs/loader.js", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "monaco loader") {
		t.Fatalf("monaco loader = %d (%s)", rec.Code, rec.Body.String())
	}
	// A nested asset resolves too.
	if rec := authGet(srv, http.MethodGet, "/ide/vs/editor/editor.main.js", ""); rec.Code != http.StatusOK {
		t.Fatalf("nested monaco asset = %d", rec.Code)
	}
}
