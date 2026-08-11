//go:build e2e

// Package mcp_test end-to-end exercise: it builds the real leoflow-mcp binary,
// launches it the way an MCP client (Claude Desktop/Code) would — as a stdio
// subprocess over the actual MCP protocol — and drives its tools and resources
// against a seeded control plane. This is the "realistic agent against the
// deployed binary" gate: it catches wiring/protocol/transport bugs that the
// in-process handler tests (which call NewServer directly) cannot.
//
// Opt-in (it compiles a binary and spawns it): `go test -tags e2e ./internal/mcp/`
// or `make test-mcp-e2e`.
package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// seededControlPlane emulates the /api/v2 surface for one DAG "etl" whose run
// "r1" failed at task "load" (try 2) with a recognizable error in its log.
func seededControlPlane(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags":
			_, _ = w.Write([]byte(`{"dags":[{"dag_id":"etl","is_paused":false}],"total_entries":1}`))
		case "/api/v2/dags/etl/dagRuns/r1":
			_, _ = w.Write([]byte(`{"dag_id":"etl","dag_run_id":"r1","state":"failed","run_type":"manual"}`))
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances":
			_, _ = w.Write([]byte(`{"task_instances":[` +
				`{"task_id":"extract","state":"success","try_number":1},` +
				`{"task_id":"load","state":"failed","try_number":2,"duration":3.5}],"total_entries":2}`))
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/logs/2":
			_, _ = w.Write([]byte("connecting\nValueError: boom in load\ndone\n"))
		default:
			t.Errorf("control plane got unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func buildMCPBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "leoflow-mcp")
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "github.com/neochaotic/leoflow/cmd/leoflow-mcp").CombinedOutput()
	if err != nil {
		t.Fatalf("building leoflow-mcp: %v\n%s", err, out)
	}
	return bin
}

func textOf(contents []mcpsdk.Content) string {
	var b strings.Builder
	for _, c := range contents {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestMCPBinaryEndToEnd(t *testing.T) {
	cp := seededControlPlane(t)
	defer cp.Close()
	bin := buildMCPBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "--server", cp.URL)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "e2e-client", Version: "0"}, nil)
	sess, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connecting to leoflow-mcp binary: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// tools/list — the three read tools are discoverable over the real protocol.
	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{"list_dags": false, "diagnose_run": false, "search_logs": false}
	for _, tl := range tools.Tools {
		if _, ok := want[tl.Name]; ok {
			want[tl.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %q not advertised by the binary", name)
		}
	}

	// diagnose_run — the highest-value flow: ask why r1 failed, get the failed
	// task and its log, end to end through the real binary.
	res, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "diagnose_run",
		Arguments: map[string]any{"dag_id": "etl", "run_id": "r1"},
	})
	if err != nil {
		t.Fatalf("CallTool diagnose_run: %v", err)
	}
	if res.IsError {
		t.Fatalf("diagnose_run returned an error result: %s", textOf(res.Content))
	}
	got := textOf(res.Content)
	for _, want := range []string{`"run_state":"failed"`, `"task_id":"load"`, "ValueError: boom in load"} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnose_run output missing %q; got: %s", want, got)
		}
	}

	// A resource read over the real protocol.
	rr, err := sess.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "run://detail/etl/r1"})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(rr.Contents) == 0 || !strings.Contains(rr.Contents[0].Text, `"state":"failed"`) {
		t.Errorf("run://detail content missing state; got %+v", rr.Contents)
	}
}
