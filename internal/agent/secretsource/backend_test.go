package secretsource

import "testing"

// A Backend is operator-configured (Helm), never author-supplied. SecretID maps a
// leoflow name to the provider secret id via the operator's prefix; the leoflow
// name never carries a provider path.
func TestBackendSecretIDMapsViaPrefix(t *testing.T) {
	b := Backend{
		Connections:       true,
		Variables:         true,
		ConnectionsPrefix: "airflow/connections",
		VariablesPrefix:   "airflow/variables",
	}
	if got := b.SecretID("databricks", KindConnection); got != "airflow/connections/databricks" {
		t.Errorf("SecretID(conn) = %q, want airflow/connections/databricks", got)
	}
	if got := b.SecretID("region", KindVariable); got != "airflow/variables/region" {
		t.Errorf("SecretID(var) = %q, want airflow/variables/region", got)
	}
}

// A backend enabled for a flat namespace (no prefix) maps the name through
// unchanged, never producing a leading separator.
func TestBackendSecretIDEmptyPrefix(t *testing.T) {
	b := Backend{Connections: true}
	if got := b.SecretID("databricks", KindConnection); got != "databricks" {
		t.Errorf("SecretID with empty prefix = %q, want databricks", got)
	}
}

// Covers is the ADR 0055 D6 coverage predicate: whether this backend serves a
// kind. It derives only from operator config (the enabled flags), so a DAG
// cannot make an arbitrary name "covered". A backend serves a kind only when the
// operator enabled it — a prefix alone is not enough, and a zero (unconfigured)
// Backend serves nothing so registration never accepts an undeclared external
// name by accident.
func TestBackendCovers(t *testing.T) {
	connOnly := Backend{Connections: true, ConnectionsPrefix: "airflow/connections"}
	if !connOnly.Covers(KindConnection) {
		t.Error("a backend enabled for connections must cover connections")
	}
	if connOnly.Covers(KindVariable) {
		t.Error("a backend not enabled for variables must not cover variables")
	}

	var zero Backend
	if zero.Covers(KindConnection) || zero.Covers(KindVariable) {
		t.Error("a zero (unconfigured) Backend must cover nothing")
	}
}
