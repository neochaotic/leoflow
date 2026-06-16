package domain

import (
	"reflect"
	"strings"
	"testing"
)

// TestEffectiveDependencies_ExpandsConnectorsThenAppendsDependencies pins the
// ADR 0038 contract: the `connectors:` short names are expanded to their
// apache-airflow-providers-* packages first, then the explicit `dependencies:`
// are appended verbatim. The order (providers before raw deps) is what lets an
// advanced user pin a transitive driver in dependencies and have pip resolve it
// against the provider declared via the sugar.
func TestEffectiveDependencies_ExpandsConnectorsThenAppendsDependencies(t *testing.T) {
	c := &LeoflowConfig{
		Connectors:   []string{"postgres", "http"},
		Dependencies: []string{"pandas==2.1.0"},
	}
	got, err := c.EffectiveDependencies()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"apache-airflow-providers-postgres",
		"apache-airflow-providers-http",
		"pandas==2.1.0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveDependencies() = %v, want %v", got, want)
	}
}

// TestEffectiveDependencies_NoConnectorsIsJustDependencies guards the common
// case (no sugar): the result is exactly `dependencies:`, untouched.
func TestEffectiveDependencies_NoConnectorsIsJustDependencies(t *testing.T) {
	c := &LeoflowConfig{Dependencies: []string{"requests"}}
	got, err := c.EffectiveDependencies()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"requests"}) {
		t.Errorf("EffectiveDependencies() = %v, want [requests]", got)
	}
}

// TestEffectiveDependencies_UnknownConnectorIsActionableError is the #2
// validation: a typo'd / unsupported connector name fails compile with a message
// that names the offender, lists the known types, and points at the
// dependencies: escape hatch — never a silent drop that becomes a runtime
// ModuleNotFoundError inside the task pod.
func TestEffectiveDependencies_UnknownConnectorIsActionableError(t *testing.T) {
	c := &LeoflowConfig{Connectors: []string{"postgres", "potgres"}}
	_, err := c.EffectiveDependencies()
	if err == nil {
		t.Fatal("expected an error for an unknown connector, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"potgres", "known:", "postgres", "dependencies:"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

// TestEffectiveDependencies_EmptyConfigIsEmpty makes sure a bare config yields
// no deps (not a nil/non-nil surprise that callers have to special-case).
func TestEffectiveDependencies_EmptyConfigIsEmpty(t *testing.T) {
	c := &LeoflowConfig{}
	got, err := c.EffectiveDependencies()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("EffectiveDependencies() = %v, want empty", got)
	}
}
