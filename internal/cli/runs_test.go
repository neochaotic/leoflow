package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// seedSessionConfig writes the config file `leoflow auth login` produces — a
// server_url plus the JWT it obtained — and returns its path. Tests point the
// CLI at it with --config so nothing reads the developer's real ~/.leoflow.
func seedSessionConfig(t *testing.T, serverURL, token string) string {
	t.Helper()
	// An ambient LEOFLOW_TOKEN would mask the config fallback under test.
	// Viper treats an empty env var as unset, so this isolates both the flag
	// default and the config lookup.
	t.Setenv("LEOFLOW_TOKEN", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "server_url: " + serverURL + "\ntoken: " + token + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return path
}

// authRecorder is an httptest server that records the Authorization header of
// the last request it served, together with a canned JSON body.
func authRecorder(t *testing.T, body string) (srv *httptest.Server, got *string) {
	t.Helper()
	var auth string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

// TestRunsTriggerUsesConfigToken pins the contract that a user who just ran
// `leoflow auth login` can trigger a run without repeating the credential:
// the persisted token must reach the control plane as a bearer token.
func TestRunsTriggerUsesConfigToken(t *testing.T) {
	srv, gotAuth := authRecorder(t, `{"dag_run_id":"run-1","state":"queued"}`)
	cfgPath := seedSessionConfig(t, srv.URL, "jwt-from-config")

	if _, _, err := run(t, "runs", "trigger", "etl", "--config", cfgPath); err != nil {
		t.Fatalf("runs trigger with a logged-in session: %v", err)
	}
	if *gotAuth != "Bearer jwt-from-config" {
		t.Errorf("auth = %q, want the token persisted by login (Bearer jwt-from-config)", *gotAuth)
	}
}

// TestRunsStatusUsesConfigToken is the read-side counterpart of
// TestRunsTriggerUsesConfigToken.
func TestRunsStatusUsesConfigToken(t *testing.T) {
	srv, gotAuth := authRecorder(t, `{"dag_runs":[{"dag_run_id":"run-1","state":"success"}]}`)
	cfgPath := seedSessionConfig(t, srv.URL, "jwt-from-config")

	if _, _, err := run(t, "runs", "status", "etl", "--config", cfgPath); err != nil {
		t.Fatalf("runs status with a logged-in session: %v", err)
	}
	if *gotAuth != "Bearer jwt-from-config" {
		t.Errorf("auth = %q, want the token persisted by login (Bearer jwt-from-config)", *gotAuth)
	}
}

// TestRunsStatusByRunIDUsesConfigToken covers the --run branch, which reaches
// the control plane through a different helper than the latest-run listing.
func TestRunsStatusByRunIDUsesConfigToken(t *testing.T) {
	srv, gotAuth := authRecorder(t, `{"dag_run_id":"run-7","state":"running"}`)
	cfgPath := seedSessionConfig(t, srv.URL, "jwt-from-config")

	if _, _, err := run(t, "runs", "status", "etl", "--run", "run-7", "--config", cfgPath); err != nil {
		t.Fatalf("runs status --run with a logged-in session: %v", err)
	}
	if *gotAuth != "Bearer jwt-from-config" {
		t.Errorf("auth = %q, want the token persisted by login (Bearer jwt-from-config)", *gotAuth)
	}
}

// TestRunsTriggerFlagTokenBeatsConfig pins the top of the precedence chain:
// an explicit --token overrides whatever the config file holds.
func TestRunsTriggerFlagTokenBeatsConfig(t *testing.T) {
	srv, gotAuth := authRecorder(t, `{"dag_run_id":"run-1","state":"queued"}`)
	cfgPath := seedSessionConfig(t, srv.URL, "jwt-from-config")

	if _, _, err := run(t, "runs", "trigger", "etl", "--config", cfgPath, "--token", "flag-token"); err != nil {
		t.Fatalf("runs trigger --token: %v", err)
	}
	if *gotAuth != "Bearer flag-token" {
		t.Errorf("auth = %q, want the explicit flag to win (Bearer flag-token)", *gotAuth)
	}
}

// TestRunsStatusFlagTokenBeatsConfig is the read-side counterpart of
// TestRunsTriggerFlagTokenBeatsConfig.
func TestRunsStatusFlagTokenBeatsConfig(t *testing.T) {
	srv, gotAuth := authRecorder(t, `{"dag_runs":[{"dag_run_id":"run-1","state":"success"}]}`)
	cfgPath := seedSessionConfig(t, srv.URL, "jwt-from-config")

	if _, _, err := run(t, "runs", "status", "etl", "--config", cfgPath, "--token", "flag-token"); err != nil {
		t.Fatalf("runs status --token: %v", err)
	}
	if *gotAuth != "Bearer flag-token" {
		t.Errorf("auth = %q, want the explicit flag to win (Bearer flag-token)", *gotAuth)
	}
}

// TestRunsTriggerEnvTokenBeatsConfig pins the middle of the precedence chain:
// LEOFLOW_TOKEN overrides the config file but yields to an explicit --token.
func TestRunsTriggerEnvTokenBeatsConfig(t *testing.T) {
	srv, gotAuth := authRecorder(t, `{"dag_run_id":"run-1","state":"queued"}`)
	cfgPath := seedSessionConfig(t, srv.URL, "jwt-from-config")
	t.Setenv("LEOFLOW_TOKEN", "env-token")

	if _, _, err := run(t, "runs", "trigger", "etl", "--config", cfgPath); err != nil {
		t.Fatalf("runs trigger with LEOFLOW_TOKEN: %v", err)
	}
	if *gotAuth != "Bearer env-token" {
		t.Errorf("auth = %q, want the env var to win over config (Bearer env-token)", *gotAuth)
	}
}

// TestRunsTriggerUsesConfigServerURL locks the server-URL resolution that the
// token fix must not regress: without --server the base URL comes from config.
func TestRunsTriggerUsesConfigServerURL(t *testing.T) {
	srv, _ := authRecorder(t, `{"dag_run_id":"run-1","state":"queued"}`)
	cfgPath := seedSessionConfig(t, srv.URL, "jwt-from-config")

	out, _, err := run(t, "runs", "trigger", "etl", "--config", cfgPath)
	if err != nil {
		t.Fatalf("runs trigger without --server: %v", err)
	}
	if out == "" {
		t.Error("expected the trigger confirmation, got no output")
	}
}
