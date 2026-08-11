package mcp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func readReq(uri string) *mcpsdk.ReadResourceRequest {
	return &mcpsdk.ReadResourceRequest{Params: &mcpsdk.ReadResourceParams{URI: uri}}
}

func TestReadDagListResource(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"dags":[{"dag_id":"etl","is_paused":false}],"total_entries":1}`)
	})
	res, err := h.readDagList(context.Background(), readReq("dag://list"))
	if err != nil {
		t.Fatalf("readDagList: %v", err)
	}
	if len(res.Contents) != 1 || !strings.Contains(res.Contents[0].Text, `"dag_id":"etl"`) {
		t.Errorf("contents = %+v, want the etl dag", res.Contents)
	}
	if res.Contents[0].URI != "dag://list" {
		t.Errorf("content URI = %q", res.Contents[0].URI)
	}
}

func TestReadRunDetailResource(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags/etl/dagRuns/r1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"dag_id":"etl","dag_run_id":"r1","state":"success","run_type":"manual"}`)
	})
	res, err := h.readRunDetail(context.Background(), readReq("run://detail/etl/r1"))
	if err != nil {
		t.Fatalf("readRunDetail: %v", err)
	}
	if !strings.Contains(res.Contents[0].Text, `"state":"success"`) {
		t.Errorf("detail missing state: %s", res.Contents[0].Text)
	}
}

func TestReadTaskInstancesResource(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags/etl/dagRuns/r1/taskInstances" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"task_instances":[{"task_id":"load","state":"failed","try_number":2}],"total_entries":1}`)
	})
	res, err := h.readTaskInstances(context.Background(), readReq("task://instances/etl/r1"))
	if err != nil {
		t.Fatalf("readTaskInstances: %v", err)
	}
	if !strings.Contains(res.Contents[0].Text, `"task_id":"load"`) {
		t.Errorf("missing task: %s", res.Contents[0].Text)
	}
}

func TestReadTaskLogResource(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/logs/2" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, "boot\nrunning\x1b[0m\ndone\n")
	})
	res, err := h.readTaskLog(context.Background(), readReq("log://task/etl/r1/load/2"))
	if err != nil {
		t.Fatalf("readTaskLog: %v", err)
	}
	if res.Contents[0].MIMEType != "text/plain" {
		t.Errorf("mime = %q, want text/plain", res.Contents[0].MIMEType)
	}
	if strings.Contains(res.Contents[0].Text, "\x1b") {
		t.Errorf("log resource must be sanitized: %q", res.Contents[0].Text)
	}
}

// TestReadResourceBadURI: a URI that does not match the template (wrong segment
// count) is a clear error, not a wrong-endpoint read.
func TestReadResourceBadURI(t *testing.T) {
	h := &handlers{}
	if _, err := h.readRunDetail(context.Background(), readReq("run://detail/etl")); err == nil {
		t.Error("expected an error for a run URI missing the run_id segment")
	}
	if _, err := h.readTaskLog(context.Background(), readReq("log://task/etl/r1/load/notanint")); err == nil {
		t.Error("expected an error for a non-integer try_number")
	}
}
