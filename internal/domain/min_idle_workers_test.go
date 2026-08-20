package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDAGSpecMinIdleWorkers locks the per-DAG author-declared warm-worker target
// (ADR 0058 N1b2b, model A2): it round-trips through JSON under the
// min_idle_workers key and is omitted when unset (0), mirroring max_active_tasks
// so a DAG that declares no warmth serializes exactly as before.
func TestDAGSpecMinIdleWorkers(t *testing.T) {
	in := DAGSpec{DagID: "d", MinIdleWorkers: 3}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"min_idle_workers":3`) {
		t.Errorf("marshaled spec = %s, want it to carry min_idle_workers:3", b)
	}
	var out DAGSpec
	if uerr := json.Unmarshal(b, &out); uerr != nil {
		t.Fatalf("unmarshal: %v", uerr)
	}
	if out.MinIdleWorkers != 3 {
		t.Errorf("round-tripped MinIdleWorkers = %d, want 3", out.MinIdleWorkers)
	}

	// Unset (0) is omitted: a DAG that declares no warmth is byte-identical to a
	// pre-N1b2b artifact.
	zero, err := json.Marshal(DAGSpec{DagID: "d"})
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	if strings.Contains(string(zero), "min_idle_workers") {
		t.Errorf("marshaled zero spec = %s, want min_idle_workers omitted", zero)
	}
}
