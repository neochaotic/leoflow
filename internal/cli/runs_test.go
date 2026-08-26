package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestRunsListRegistered pins the ergonomics fix that `leoflow runs list`
// exists: users reach for it before discovering the operator alias
// `leoflow admin runs list`. It must resolve to a `list` subcommand under
// `runs`, not fall back to the `runs` group itself.
func TestRunsListRegistered(t *testing.T) {
	root := NewRootCommand()
	cmd, _, err := root.Find([]string{"runs", "list"})
	if err != nil {
		t.Fatalf("finding `runs list`: %v", err)
	}
	if cmd.Name() != "list" {
		t.Fatalf("`runs list` resolved to %q, want a registered `list` subcommand", cmd.Name())
	}
}

// TestRunsListInvokesLister proves `runs list` drives the same lister as
// `admin runs list`: the same server-side --state filter, the same --older-than
// age filter, and the same tabular output. It reuses runsServer, the mock
// control plane shared with the admin runs tests.
func TestRunsListInvokesLister(t *testing.T) {
	now := time.Now()
	srv := runsServer(t, now)
	defer srv.Close()

	out, _, err := run(t, "runs", "list", "--state", "running", "--older-than", "2h",
		"--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("runs list: %v", err)
	}
	if !strings.Contains(out, "run-old") {
		t.Errorf("output = %q, want it to include run-old", out)
	}
	if strings.Contains(out, "run-new") || strings.Contains(out, "run-done") {
		t.Errorf("output = %q, should have filtered out run-new and run-done", out)
	}
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

// logsServer mounts the Airflow-compatible task-instance endpoints the
// `runs logs` command talks to: the single task-instance lookup (which carries
// try_number, used to resolve the latest attempt) and the catch-all logs route
// (`.../logs/{try}`). Each attempt's body is keyed by its try number so a test
// can prove which attempt was streamed; the special try -1 stands in for the
// graceful "no logs" passthrough. It records the last logs path + Authorization
// header it served.
func logsServer(t *testing.T, tryNumber int, bodyByTry map[int]string) (srv *httptest.Server, gotLogsPath, gotAuth *string) {
	t.Helper()
	var logsPath, auth string
	mux := http.NewServeMux()
	// GET .../taskInstances/etl-task -> the instance, carrying its try_number.
	mux.HandleFunc("/api/v2/dags/etl/dagRuns/run-1/taskInstances/etl-task",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v2/dags/etl/dagRuns/run-1/taskInstances/etl-task" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"task_id":"etl-task","try_number":` + itoa(tryNumber) + `}`))
		})
	// GET .../taskInstances/etl-task/logs/{try} -> the attempt's plain-text logs.
	mux.HandleFunc("/api/v2/dags/etl/dagRuns/run-1/taskInstances/etl-task/logs/",
		func(w http.ResponseWriter, r *http.Request) {
			logsPath = r.URL.Path
			auth = r.Header.Get("Authorization")
			try := 0
			_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path,
				"/api/v2/dags/etl/dagRuns/run-1/taskInstances/etl-task/logs/"), "%d", &try)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if body, ok := bodyByTry[try]; ok {
				_, _ = w.Write([]byte(body))
				return
			}
			_, _ = w.Write([]byte("No logs available for this attempt.\n"))
		})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &logsPath, &auth
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// TestRunsLogsRegistered pins that `leoflow runs logs` exists as a subcommand
// under `runs`, next to `trigger`/`status`/`list`.
func TestRunsLogsRegistered(t *testing.T) {
	root := NewRootCommand()
	cmd, _, err := root.Find([]string{"runs", "logs"})
	if err != nil {
		t.Fatalf("finding `runs logs`: %v", err)
	}
	if cmd.Name() != "logs" {
		t.Fatalf("`runs logs` resolved to %q, want a registered `logs` subcommand", cmd.Name())
	}
}

// TestRunsLogsStreamsAttempt proves the command hits the task-instance logs
// endpoint for the requested --try, injects the bearer token, and streams the
// body to stdout.
func TestRunsLogsStreamsAttempt(t *testing.T) {
	srv, gotPath, gotAuth := logsServer(t, 2, map[int]string{2: "boot\nrunning task\ndone\n"})

	out, _, err := run(t, "runs", "logs", "etl", "run-1", "etl-task",
		"--try", "2", "--server", srv.URL, "--token", "jwt-xyz")
	if err != nil {
		t.Fatalf("runs logs: %v", err)
	}
	if *gotPath != "/api/v2/dags/etl/dagRuns/run-1/taskInstances/etl-task/logs/2" {
		t.Errorf("hit %q, want the try-2 logs endpoint", *gotPath)
	}
	if *gotAuth != "Bearer jwt-xyz" {
		t.Errorf("auth = %q, want Bearer jwt-xyz", *gotAuth)
	}
	if !strings.Contains(out, "running task") || !strings.Contains(out, "done") {
		t.Errorf("output = %q, want the streamed log body", out)
	}
}

// TestRunsLogsDefaultsToLatestTry proves that with no --try the command reads
// the task instance's current try_number and streams that attempt.
func TestRunsLogsDefaultsToLatestTry(t *testing.T) {
	srv, gotPath, _ := logsServer(t, 3, map[int]string{3: "third attempt output\n"})

	out, _, err := run(t, "runs", "logs", "etl", "run-1", "etl-task",
		"--server", srv.URL, "--token", "t")
	if err != nil {
		t.Fatalf("runs logs (default try): %v", err)
	}
	if *gotPath != "/api/v2/dags/etl/dagRuns/run-1/taskInstances/etl-task/logs/3" {
		t.Errorf("hit %q, want the latest attempt (try 3) resolved from the task instance", *gotPath)
	}
	if !strings.Contains(out, "third attempt output") {
		t.Errorf("output = %q, want the latest attempt's logs", out)
	}
}

// TestRunsLogsNoLogsMessagePassesThrough proves the endpoint's graceful
// "No logs available for this attempt." body reaches stdout as-is, not an error.
func TestRunsLogsNoLogsMessagePassesThrough(t *testing.T) {
	srv, _, _ := logsServer(t, 1, map[int]string{}) // no body for any try -> the graceful message

	out, _, err := run(t, "runs", "logs", "etl", "run-1", "etl-task",
		"--try", "1", "--server", srv.URL, "--token", "t")
	if err != nil {
		t.Fatalf("the no-logs path must not error, got %v", err)
	}
	if !strings.Contains(out, "No logs available for this attempt.") {
		t.Errorf("output = %q, want the graceful no-logs message passed through", out)
	}
}

// TestRunsLogsUsesConfigToken is the logs-side counterpart of the trigger/status
// config-token tests: a logged-in session's persisted token must reach the
// endpoint without repeating the credential.
func TestRunsLogsUsesConfigToken(t *testing.T) {
	srv, _, gotAuth := logsServer(t, 1, map[int]string{1: "ok\n"})
	cfgPath := seedSessionConfig(t, srv.URL, "jwt-from-config")

	if _, _, err := run(t, "runs", "logs", "etl", "run-1", "etl-task", "--try", "1", "--config", cfgPath); err != nil {
		t.Fatalf("runs logs with a logged-in session: %v", err)
	}
	if *gotAuth != "Bearer jwt-from-config" {
		t.Errorf("auth = %q, want the token persisted by login (Bearer jwt-from-config)", *gotAuth)
	}
}

// TestRunsLogsErrorsOnServerFailure pins that a non-2xx (that is not the
// graceful no-logs 200) surfaces as a CLI error rather than empty success.
func TestRunsLogsErrorsOnServerFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, _, err := run(t, "runs", "logs", "etl", "run-1", "etl-task", "--try", "1", "--server", srv.URL); err == nil {
		t.Error("a non-2xx logs response should error")
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
