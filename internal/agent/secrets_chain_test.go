package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/neochaotic/leoflow/internal/agent/secretsource"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeResolver stands in for the pod-side external backend.
type fakeResolver struct {
	vars  map[string]string
	conns map[string]string
	err   error
}

func (f fakeResolver) Resolve(_ context.Context, name string, kind secretsource.Kind) (value string, found bool, err error) {
	if f.err != nil {
		return "", false, f.err
	}
	if kind == secretsource.KindConnection {
		v, ok := f.conns[name]
		return v, ok, nil
	}
	v, ok := f.vars[name]
	return v, ok, nil
}

// With no resolver configured, the chain is the vault only — byte-identical to the
// pre-ADR-0060 env-export.
func TestSecretsEnvNoResolverVaultOnly(t *testing.T) {
	r := &Runner{Client: &fakeClient{vars: map[string]string{"a": "1"}, conns: map[string]string{"db": "postgres://x"}}}
	out, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{DeclaredVariables: []string{"a"}})
	if err != nil {
		t.Fatalf("secretsEnv: %v", err)
	}
	if !slices.Contains(out, "AIRFLOW_VAR_A=1") || !slices.Contains(out, "AIRFLOW_CONN_DB=postgres://x") {
		t.Errorf("vault-only env missing entries: %v", out)
	}
}

// A declared name the backend covers is resolved externally and OVERRIDES the
// vault entry for that name.
func TestSecretsEnvExternalOverridesVault(t *testing.T) {
	r := &Runner{
		Client:        &fakeClient{vars: map[string]string{"region": "vault-val"}},
		Resolver:      fakeResolver{vars: map[string]string{"region": "ext-val"}},
		SecretBackend: secretsource.Backend{Variables: true},
	}
	out, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{DeclaredVariables: []string{"region"}})
	if err != nil {
		t.Fatalf("secretsEnv: %v", err)
	}
	if !slices.Contains(out, "AIRFLOW_VAR_REGION=ext-val") || slices.Contains(out, "AIRFLOW_VAR_REGION=vault-val") {
		t.Errorf("external hit must override vault: %v", out)
	}
}

// A hard resolver error (not a clean miss) fails the task closed (B6).
func TestSecretsEnvHardResolverErrorFailsClosed(t *testing.T) {
	r := &Runner{
		Client:        &fakeClient{},
		Resolver:      fakeResolver{err: errors.New("access denied")},
		SecretBackend: secretsource.Backend{Variables: true},
	}
	_, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{DeclaredVariables: []string{"x"}})
	if err == nil {
		t.Fatal("a hard resolver error must fail the task closed")
	}
}

// A liveness-enforce PermissionDenied on a vault RPC skips external resolution
// entirely — a non-live TI resolves nothing (B2).
func TestSecretsEnvVaultDeniedSkipsExternal(t *testing.T) {
	r := &Runner{
		Client:        &fakeClient{getVarsErr: status.Error(codes.PermissionDenied, "task instance not live")},
		Resolver:      fakeResolver{vars: map[string]string{"x": "ext"}},
		SecretBackend: secretsource.Backend{Variables: true},
	}
	out, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{DeclaredVariables: []string{"x"}})
	if err != nil {
		t.Fatalf("secretsEnv (denied vault) should be best-effort, not error: %v", err)
	}
	if slices.Contains(out, "AIRFLOW_VAR_X=ext") {
		t.Error("external resolution must be skipped when the vault RPC is PermissionDenied (B2)")
	}
}
