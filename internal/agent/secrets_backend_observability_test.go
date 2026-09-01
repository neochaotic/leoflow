package agent

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/agent/secretsource"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

// captureWarn swaps the default slog logger for a buffer for the duration of a
// test and returns the buffer.
func captureWarn(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// With a backend configured, a declared name that resolves from NEITHER the
// backend NOR the vault must be logged with a pointer at the pod's identity —
// the top field-support signal (#1): a backend auth failure surfaces as a miss,
// so the task otherwise fails downstream with a bare "not delivered".
func TestSecretsEnvWarnsOnUnresolvedDeclaredWithBackend(t *testing.T) {
	buf := captureWarn(t)
	r := &Runner{
		Client:        &fakeClient{},  // empty vault
		Resolver:      fakeResolver{}, // no external hits
		SecretBackend: secretsource.Backend{Connections: true, Variables: true},
	}
	if _, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{DeclaredConnections: []string{"warehouse"}}); err != nil {
		t.Fatalf("secretsEnv: %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "unresolved") || !strings.Contains(logged, "warehouse") {
		t.Errorf("expected a warn naming the unresolved declared connection; got: %q", logged)
	}
	if !strings.Contains(logged, "identity") {
		t.Errorf("warn should point at the pod's identity/permissions; got: %q", logged)
	}
}

// When the backend resolves the declared name, there is nothing unresolved and no
// warning — the common case must stay quiet.
func TestSecretsEnvNoWarnWhenResolved(t *testing.T) {
	buf := captureWarn(t)
	r := &Runner{
		Client:        &fakeClient{},
		Resolver:      fakeResolver{conns: map[string]string{"warehouse": "postgres://w"}},
		SecretBackend: secretsource.Backend{Connections: true},
	}
	if _, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{DeclaredConnections: []string{"warehouse"}}); err != nil {
		t.Fatalf("secretsEnv: %v", err)
	}
	if strings.Contains(buf.String(), "unresolved") {
		t.Errorf("no unresolved warn expected when the backend resolved the name; got: %q", buf.String())
	}
}

// With NO backend configured, an unresolved declared name is the pre-existing
// vault-only behavior — the backend-identity warning must not fire.
func TestSecretsEnvNoBackendNoWarn(t *testing.T) {
	buf := captureWarn(t)
	r := &Runner{Client: &fakeClient{}} // no Resolver
	if _, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{DeclaredConnections: []string{"warehouse"}}); err != nil {
		t.Fatalf("secretsEnv: %v", err)
	}
	if strings.Contains(buf.String(), "unresolved") {
		t.Errorf("no backend configured → no backend-identity warn; got: %q", buf.String())
	}
}
