package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPushPrintsServerWarnings: the register response may carry non-fatal
// deprecation warnings (ADR 0047 http_api). `leoflow push` must surface them to
// the author on stderr — the CLI is the only place the http_api deprecation is
// visible, since the parser no longer emits it (it can only arrive via a
// hand-written dag.json). Without this the warning dies in the server log.
func TestPushPrintsServerWarnings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"dag_id":"pushdag","version":"v1","spec_hash":"abc","created":true,` +
			`"warnings":["task \"fetch\" uses the deprecated task type \"http_api\"; use an HttpOperator"]}`))
	}))
	defer srv.Close()

	f := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(f, []byte(pushSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := run(t, "push", f, "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !strings.Contains(out, "Registered") {
		t.Errorf("stdout should still confirm registration, got %q", out)
	}
	combined := out + errOut
	if !strings.Contains(combined, "deprecated") || !strings.Contains(combined, "HttpOperator") {
		t.Errorf("push did not surface the server deprecation warning; stdout=%q stderr=%q", out, errOut)
	}
}
