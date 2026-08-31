package executor

import "testing"

// The operator's external-secrets backend (ADR 0060) is injected as LEOFLOW_
// SECRETS_* pod env, in the leoflow-owned group. Empty = not present (chain stays
// vault-only). It is operator-sourced; an author cannot set LEOFLOW_ keys (#828).
func TestBuildPodSecretsBackendEnv(t *testing.T) {
	req := sampleReq()
	req.SecretsBackend = "prov.Backend"
	req.SecretsBackendKwargs = `{"connections_prefix":"airflow/connections"}`
	env := podEnvMap(BuildPod(req).Spec.Containers[0])

	if env["LEOFLOW_SECRETS_BACKEND"] != "prov.Backend" {
		t.Errorf("LEOFLOW_SECRETS_BACKEND = %q", env["LEOFLOW_SECRETS_BACKEND"])
	}
	if env["LEOFLOW_SECRETS_BACKEND_KWARGS"] != `{"connections_prefix":"airflow/connections"}` {
		t.Errorf("LEOFLOW_SECRETS_BACKEND_KWARGS = %q", env["LEOFLOW_SECRETS_BACKEND_KWARGS"])
	}
}

func TestBuildPodNoSecretsBackendByDefault(t *testing.T) {
	env := podEnvMap(BuildPod(sampleReq()).Spec.Containers[0])
	if _, ok := env["LEOFLOW_SECRETS_BACKEND"]; ok {
		t.Error("LEOFLOW_SECRETS_BACKEND must not be set when no backend is configured")
	}
}
