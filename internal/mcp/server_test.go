package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/neochaotic/leoflow/pkg/client"
)

// TestDiagnoseRun composes run + task-instances + logs into one diagnosis: it
// reports the run state, isolates the failed task, and returns a truncated,
// sanitized tail of that task's log (ADR 0050 R19). It must not treat a
// successful task as failed, and must surface the failing task's log.
func TestDiagnoseRun(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags/etl/dagRuns/r1":
			_, _ = io.WriteString(w, `{"dag_id":"etl","dag_run_id":"r1","state":"failed"}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances":
			_, _ = io.WriteString(w, `{"task_instances":[`+
				`{"task_id":"extract","state":"success","try_number":1},`+
				`{"task_id":"load","state":"failed","try_number":2,"duration":3.5}],"total_entries":2}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/logs/2":
			_, _ = io.WriteString(w, "setup\nrunning query\nTraceback (most recent call last)\nValueError: boom\x1b[0m\n")
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	_, out, err := h.diagnoseRun(context.Background(), nil, diagnoseRunInput{DagID: "etl", RunID: "r1"})
	if err != nil {
		t.Fatalf("diagnoseRun: %v", err)
	}
	if out.RunState != "failed" {
		t.Errorf("run_state = %q, want failed", out.RunState)
	}
	if out.TotalTasks != 2 {
		t.Errorf("total_tasks = %d, want 2", out.TotalTasks)
	}
	if len(out.FailedTasks) != 1 || out.FailedTasks[0].TaskID != "load" {
		t.Fatalf("failed_tasks = %+v, want exactly [load]", out.FailedTasks)
	}
	ft := out.FailedTasks[0]
	if ft.TryNumber != 2 {
		t.Errorf("try_number = %d, want 2", ft.TryNumber)
	}
	if !strings.Contains(ft.LogTail, "ValueError: boom") {
		t.Errorf("log_tail missing the failure line: %q", ft.LogTail)
	}
	if strings.Contains(ft.LogTail, "\x1b") {
		t.Errorf("log_tail must be sanitized of control/ANSI bytes: %q", ft.LogTail)
	}
}

// TestSearchLogs finds case-insensitive substring matches in one task's log,
// reports 1-based line numbers, sanitizes control/ANSI bytes, and returns the
// total found even when the returned set is capped.
func TestSearchLogs(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/logs/2" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, "starting\nERROR: connection refused\nretrying\nerror: timeout\x1b[0m\ndone\n")
	})

	_, out, err := h.searchLogs(context.Background(), nil, searchLogsInput{
		DagID: "etl", RunID: "r1", TaskID: "load", TryNumber: 2, Query: "error",
	})
	if err != nil {
		t.Fatalf("searchLogs: %v", err)
	}
	if out.TotalMatches != 2 || len(out.Matches) != 2 {
		t.Fatalf("matches = %+v (total %d), want 2", out.Matches, out.TotalMatches)
	}
	if out.Matches[0].LineNumber != 2 || out.Matches[1].LineNumber != 4 {
		t.Errorf("line numbers = %d,%d, want 2,4", out.Matches[0].LineNumber, out.Matches[1].LineNumber)
	}
	if strings.Contains(out.Matches[1].Line, "\x1b") {
		t.Errorf("match line must be sanitized: %q", out.Matches[1].Line)
	}
	if out.Truncated {
		t.Error("should not be truncated with the default cap")
	}
}

// TestSearchLogsTruncates caps the returned matches but still reports the true
// total, so the agent knows there is more.
func TestSearchLogsTruncates(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "error a\nerror b\nerror c\n")
	})
	_, out, err := h.searchLogs(context.Background(), nil, searchLogsInput{
		DagID: "d", RunID: "r", TaskID: "t", Query: "error", MaxMatches: 1,
	})
	if err != nil {
		t.Fatalf("searchLogs: %v", err)
	}
	if len(out.Matches) != 1 || out.TotalMatches != 3 || !out.Truncated {
		t.Errorf("want 1 returned / 3 total / truncated, got %d / %d / %v", len(out.Matches), out.TotalMatches, out.Truncated)
	}
}

// TestSearchLogsMissingArgs: the locators and the query are required.
func TestSearchLogsMissingArgs(t *testing.T) {
	h := &handlers{}
	if _, _, err := h.searchLogs(context.Background(), nil, searchLogsInput{DagID: "d", RunID: "r", TaskID: "t"}); err == nil {
		t.Error("expected an error when query is missing")
	}
}

// TestDiagnoseRunMissingArgs: dag_id and run_id are required.
func TestDiagnoseRunMissingArgs(t *testing.T) {
	h := &handlers{}
	if _, _, err := h.diagnoseRun(context.Background(), nil, diagnoseRunInput{DagID: "etl"}); err == nil {
		t.Error("expected an error when run_id is missing")
	}
}

// TestNewServerRegistersListDags connects a client to the server over an
// in-memory transport and asserts list_dags is discoverable via tools/list —
// the tool has to be REGISTERED, not just implemented (a registered-but-invisible
// tool is the powerlab UX trap this guards against).
func TestNewServerRegistersListDags(t *testing.T) {
	ctx := context.Background()
	api, err := apiclient.New("http://control-plane.invalid", "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	srv := NewServer(api, "test")

	serverT, clientT := mcpsdk.NewInMemoryTransports()
	_, err = srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	found := false
	for _, tool := range res.Tools {
		if tool.Name == "list_dags" {
			found = true
		}
	}
	if !found {
		t.Errorf("list_dags not registered/discoverable via tools/list; got %d tools", len(res.Tools))
	}
}

// testHandlers spins up a fake control plane and returns handlers wired to it.
func testHandlers(t *testing.T, fn http.HandlerFunc) *handlers {
	t.Helper()
	srv := httptest.NewServer(fn)
	t.Cleanup(srv.Close)
	c, err := apiclient.New(srv.URL, "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return &handlers{api: c}
}

// TestListDagsShapesOutput: the tool calls /api/v2/dags and returns a compact
// shape (dag_id + is_paused), not the verbose upstream payload (ADR 0050 D7/R18).
func TestListDagsShapesOutput(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags" {
			t.Errorf("path = %q, want /api/v2/dags", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dags":[{"dag_id":"etl","is_paused":false},{"dag_id":"ml","is_paused":true}],"total_entries":2}`))
	})

	_, out, err := h.listDags(context.Background(), nil, listDagsInput{})
	if err != nil {
		t.Fatalf("listDags: %v", err)
	}
	if out.TotalEntries != 2 || len(out.Dags) != 2 {
		t.Fatalf("output = %+v, want 2 dags", out)
	}
	if out.Dags[0].DagID != "etl" || out.Dags[0].IsPaused {
		t.Errorf("dags[0] = %+v, want etl/not-paused", out.Dags[0])
	}
	if out.Dags[1].DagID != "ml" || !out.Dags[1].IsPaused {
		t.Errorf("dags[1] = %+v, want ml/paused", out.Dags[1])
	}
}

// TestListDagsControlPlaneError: a non-200 from the control plane surfaces as an
// error, not an empty-but-successful result (so the agent does not conclude
// "there are no DAGs" when the call actually failed).
func TestListDagsControlPlaneError(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, _, err := h.listDags(context.Background(), nil, listDagsInput{}); err == nil {
		t.Error("expected an error when the control plane returns 500")
	}
}
