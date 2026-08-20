package dispatch

import (
	"context"
	"testing"
)

// TestDispatchDefaultTransportIsEnvVar: with no transport configured the request
// carries the env-var transport (empty), so BuildPod keeps today's plaintext
// token env var — the safe default.
func TestDispatchDefaultTransportIsEnvVar(t *testing.T) {
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti-1"}}, &fakeIssuer{token: "t"}, exec)
	if _, err := d.Dispatch(context.Background(), "run", "etl", "", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if exec.req.AgentTokenTransport == "exchange" {
		t.Errorf("default transport must not be exchange, got %q", exec.req.AgentTokenTransport)
	}
}

// TestDispatchExchangeTransportThreaded: when the operator selects the exchange
// transport, the dispatcher threads it (plus the projected-token audience) onto
// the request so the K8s executor projects a token instead of the plaintext one.
func TestDispatchExchangeTransportThreaded(t *testing.T) {
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti-1"}}, &fakeIssuer{token: "t"}, exec)
	d.SetAgentTokenTransport("exchange", "leoflow-control-plane", 900)
	if _, err := d.Dispatch(context.Background(), "run", "etl", "", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if exec.req.AgentTokenTransport != "exchange" {
		t.Errorf("AgentTokenTransport = %q, want exchange", exec.req.AgentTokenTransport)
	}
	if exec.req.AgentTokenAudience != "leoflow-control-plane" || exec.req.AgentTokenExpirationSeconds != 900 {
		t.Errorf("projected-token config not threaded: aud=%q exp=%d", exec.req.AgentTokenAudience, exec.req.AgentTokenExpirationSeconds)
	}
	// The plaintext token is still minted (needed by the env-var path and harmless
	// under exchange); the executor decides whether to place it on the pod.
	if exec.req.AgentToken == "" {
		t.Error("a token should still be minted at dispatch")
	}
}
