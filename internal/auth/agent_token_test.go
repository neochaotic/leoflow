package auth

import (
	"context"
	"testing"
	"time"
)

func agentIdentity() AgentIdentity {
	return AgentIdentity{
		TaskInstanceID: "ti-1",
		TenantID:       "acme",
		DagID:          "etl",
		RunID:          "run-1",
		TaskID:         "extract",
		TryNumber:      2,
	}
}

func TestAgentTokenRoundTrip(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	token, err := a.IssueAgentToken(agentIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	got, err := a.AuthenticateAgent(token)
	if err != nil {
		t.Fatalf("AuthenticateAgent: %v", err)
	}
	want := agentIdentity()
	if *got != want {
		t.Errorf("identity = %+v, want %+v", *got, want)
	}
}

func TestAgentTokenRejectsUserToken(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	userToken, err := a.sign(&User{ID: "u1", TenantID: "acme", Roles: []string{"admin"}})
	if err != nil {
		t.Fatalf("sign user: %v", err)
	}
	if _, err := a.AuthenticateAgent(userToken); err == nil {
		t.Error("a user token must not authenticate as an agent")
	}
}

func TestUserAuthRejectsAgentToken(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	agentToken, err := a.IssueAgentToken(agentIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if _, err := a.Authenticate(context.Background(), agentToken); err == nil {
		t.Error("an agent token must not authenticate as a user")
	}
}

func TestAgentTokenRejectsTampered(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	other := NewJWTAuthenticator(nil, "different-secret", time.Hour)
	token, err := other.IssueAgentToken(agentIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if _, err := a.AuthenticateAgent(token); err == nil {
		t.Error("a token signed with a different secret must be rejected")
	}
}

func TestAgentTokenRejectsExpired(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	token, err := a.IssueAgentToken(agentIdentity(), -time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if _, err := a.AuthenticateAgent(token); err == nil {
		t.Error("an expired agent token must be rejected")
	}
}

// warmWorkerIdentity is the identity ExchangeToken resolves for a warm pod: a
// worker credential that names its dag_version pool and its worker id, carries
// its tenant, and has NO task claims (no task instance, run, task, or try).
func warmWorkerIdentity() AgentIdentity {
	return AgentIdentity{
		Scope:        ScopeWarmWorker,
		DagVersionID: "dagver-9",
		TenantID:     "acme",
		WorkerID:     "leoflow-warm-dagver-9-abcd",
	}
}

// TestWarmWorkerTokenRoundTrip: a warm-worker identity is minted with the worker
// id as Subject, a scope=warm-worker claim, and dag_version_id, then verifies back
// to the SAME identity — Scope, DagVersionID, WorkerID preserved and NO task
// claims (TaskInstanceID/RunID/TaskID/TryNumber empty).
func TestWarmWorkerTokenRoundTrip(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	token, err := a.IssueAgentToken(warmWorkerIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	got, err := a.AuthenticateAgent(token)
	if err != nil {
		t.Fatalf("AuthenticateAgent: %v", err)
	}
	want := warmWorkerIdentity()
	if *got != want {
		t.Errorf("warm identity = %+v, want %+v", *got, want)
	}
	if got.Scope != ScopeWarmWorker {
		t.Errorf("scope = %q, want %q", got.Scope, ScopeWarmWorker)
	}
	// No task claims leaked onto the worker credential.
	if got.TaskInstanceID != "" || got.RunID != "" || got.TaskID != "" || got.TryNumber != 0 {
		t.Errorf("warm-worker token must carry no task claims, got %+v", *got)
	}
	if got.WorkerID != want.WorkerID {
		t.Errorf("worker id = %q, want %q (the token Subject)", got.WorkerID, want.WorkerID)
	}
}

// TestTaskTokenScopeIsEmpty: a task identity round-trips with Scope=="" exactly as
// before this field existed — the byte-compatibility guard for existing tokens.
func TestTaskTokenScopeIsEmpty(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	token, err := a.IssueAgentToken(agentIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	got, err := a.AuthenticateAgent(token)
	if err != nil {
		t.Fatalf("AuthenticateAgent: %v", err)
	}
	if got.Scope != "" {
		t.Errorf("task token scope = %q, want empty", got.Scope)
	}
	if got.WorkerID != "" || got.DagVersionID != "" {
		t.Errorf("task token must not carry worker/dag-version fields, got %+v", *got)
	}
	if got.TaskInstanceID != "ti-1" {
		t.Errorf("task instance id = %q, want ti-1 (the Subject)", got.TaskInstanceID)
	}
}
