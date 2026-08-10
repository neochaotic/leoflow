package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/neochaotic/leoflow/pkg/client"
)

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
