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
