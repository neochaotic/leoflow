package domain

import "testing"

// A leoflow.yaml declaring a dbt project (ADR 0042) validates against the schema.
func TestDbtConfigValidates(t *testing.T) {
	c := &LeoflowConfig{
		DagID: "sales",
		Dbt:   &DbtConfig{Project: "./analytics", Granularity: "level"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid dbt config rejected: %v", err)
	}
}

// An unknown granularity is rejected by the schema enum, not silently accepted.
func TestDbtConfigRejectsUnknownGranularity(t *testing.T) {
	c := &LeoflowConfig{
		DagID: "sales",
		Dbt:   &DbtConfig{Project: "./analytics", Granularity: "bogus"},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected unknown granularity to be rejected")
	}
}
