package secretsource

import "testing"

// ParseBackendConfig reads the operator-supplied external-secrets config (from the
// LEOFLOW_SECRETS_* pod env, operator-only). An empty class means no external
// backend (enabled=false → the resolver is never built, chain is vault-only).
// Routing (Covers) is derived from which *_prefix kwargs the operator set, mirroring
// Airflow's enable-by-prefix: a kind is covered iff its prefix kwarg is present.
func TestParseBackendConfigDisabledWhenNoClass(t *testing.T) {
	_, enabled, err := ParseBackendConfig("", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if enabled {
		t.Error("no backend class must mean disabled")
	}
}

func TestParseBackendConfigBothKinds(t *testing.T) {
	cfg, enabled, err := ParseBackendConfig(
		"airflow.providers.amazon.aws.secrets.secrets_manager.SecretsManagerBackend",
		`{"connections_prefix":"airflow/connections","variables_prefix":"airflow/variables","region_name":"us-east-1"}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !enabled {
		t.Fatal("a configured class must enable the backend")
	}
	if cfg.Class == "" {
		t.Error("class must be carried for the subprocess")
	}
	if !cfg.Routing.Covers(KindConnection) || !cfg.Routing.Covers(KindVariable) {
		t.Error("both kinds must be covered when both prefixes are set")
	}
	if got := cfg.Routing.SecretID("db", KindConnection); got != "airflow/connections/db" {
		t.Errorf("SecretID = %q", got)
	}
}

func TestParseBackendConfigConnectionsOnly(t *testing.T) {
	cfg, enabled, err := ParseBackendConfig("some.Backend", `{"connections_prefix":"c"}`)
	if err != nil || !enabled {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	if !cfg.Routing.Covers(KindConnection) || cfg.Routing.Covers(KindVariable) {
		t.Error("only connections must be covered when only connections_prefix is set")
	}
}

func TestParseBackendConfigBadKwargs(t *testing.T) {
	if _, _, err := ParseBackendConfig("some.Backend", "{not json"); err == nil {
		t.Error("malformed kwargs JSON must error (fail closed at config time)")
	}
}
