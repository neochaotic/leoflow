package dbt

import (
	"testing"
)

// TestCompileCarriesDeclaredSecrets is the regression for #997. Compile builds
// its DAGSpec from an explicit field list, and that list had no Connections or
// Variables — so a leoflow.yaml declaring them produced a dag.json without them
// on the dbt-only path, while the dag.py path emitted both.
//
// Under ADR 0055 secret scoping the consequence is not cosmetic: the task pod
// receives NOTHING, because what it may see is derived from what the spec
// declares. The DAG that asked for a warehouse connection runs without it, and
// the failure appears inside the task.
func TestCompileCarriesDeclaredSecrets(t *testing.T) {
	manifest := []byte(`{"nodes":{"model.shop.orders":{"resource_type":"model","name":"orders","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["shop","orders"]}}}`)

	t.Run("declared connections and variables reach the spec", func(t *testing.T) {
		spec, err := Compile(manifest, Meta{
			DagID:       "shop",
			Connections: []string{"warehouse", "reporting"},
			Variables:   []string{"env"},
		})
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if len(spec.Connections) != 2 || spec.Connections[0] != "warehouse" {
			t.Errorf("spec.Connections = %v, want the declared pair", spec.Connections)
		}
		if len(spec.Variables) != 1 || spec.Variables[0] != "env" {
			t.Errorf("spec.Variables = %v, want [env]", spec.Variables)
		}
	})

	t.Run("nothing declared leaves both absent", func(t *testing.T) {
		// The fields are omitempty, and an empty slice would serialize
		// differently from an absent one — a needless diff in every dag.json.
		spec, err := Compile(manifest, Meta{DagID: "shop"})
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if spec.Connections != nil || spec.Variables != nil {
			t.Errorf("want both nil when nothing is declared; got %v / %v", spec.Connections, spec.Variables)
		}
	})

	t.Run("the caller's slice is not aliased", func(t *testing.T) {
		// Compile's result outlives the Meta it was built from; sharing the
		// backing array lets a later append by the caller mutate the spec.
		conns := make([]string, 1, 4)
		conns[0] = "warehouse"
		spec, err := Compile(manifest, Meta{DagID: "shop", Connections: conns})
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		conns = append(conns, "sneaked")
		_ = conns
		if len(spec.Connections) != 1 || spec.Connections[0] != "warehouse" {
			t.Errorf("the spec changed under the caller: %v", spec.Connections)
		}
	})
}
