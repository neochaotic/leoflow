package domain

import (
	"strings"
	"testing"
)

// TestHTTPAPITaskTypeRejected pins the ADR 0047/0048 removal (#512): the native
// inline http_api task type no longer exists, so a spec declaring it — the only
// way it can now arrive is a hand-written dag.json — is REJECTED at validation
// (the structural guard: the SSRF path is gone because the type cannot be
// registered, not merely warned about). HttpOperator runs in a pod as
// airflow_operator instead.
func TestHTTPAPITaskTypeRejected(t *testing.T) {
	spec := &DAGSpec{
		SchemaVersion: "1.0", DagID: "d", DagVersion: "v1", Image: "img:v1",
		Tasks: []TaskSpec{
			{TaskID: "fetch", Type: "http_api"},
		},
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("a spec with a task type=http_api must be REJECTED (removed, ADR 0047/#512), not accepted")
	}
	if !strings.Contains(err.Error(), "http_api") {
		t.Errorf("rejection error should name http_api, got: %v", err)
	}
}
