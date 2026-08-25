package domain

import "testing"

// A leoflow.yaml `defaults` block declaring resources and node_selector (the
// DAG-wide placement/QoS fallback, EKS validation aresta #6) validates against
// the canonical schema.
func TestDefaultsBlockWithNodeSelectorValidates(t *testing.T) {
	c := &LeoflowConfig{
		DagID: "sales",
		Defaults: &ConfigDefaults{
			Resources:    &DefaultResources{CPU: "1", Memory: "1Gi"},
			NodeSelector: map[string]string{"disktype": "ssd"},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid defaults block rejected: %v", err)
	}
}

// An unknown key under `defaults` fails validation loudly
// (additionalProperties:false) instead of being silently accepted-and-dropped —
// the exact footgun that hid node_selector before it became a wired field. This
// guards the whole "accepted but never reaches the pod" class: a future unwired
// default key fails at compile, not at runtime.
func TestDefaultsBlockUnknownKeyRejected(t *testing.T) {
	s, err := schemas()
	if err != nil {
		t.Fatalf("compile schemas: %v", err)
	}
	inst := map[string]any{
		"dag_id": "sales",
		"defaults": map[string]any{
			"resources": map[string]any{"cpu": "1", "memory": "1Gi"},
			// Typo / unwired key: must be rejected, not silently discarded.
			"nodeselector": map[string]any{"disktype": "ssd"},
		},
	}
	if verr := validateAgainst(s.leoflow, inst); verr == nil {
		t.Fatal("expected unknown key under defaults to be rejected (additionalProperties:false)")
	}
}
