package config

import (
	"testing"
	"time"
)

// TestMaxAttemptCredentialLifetimeDefault: the ceiling defaults generous (24h)
// so per-attempt token renewal never regresses a normal long task.
func TestMaxAttemptCredentialLifetimeDefault(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if got := c.Auth.MaxAttemptCredentialLifetime; got != 24*time.Hour {
		t.Errorf("auth.max_attempt_credential_lifetime default = %v, want 24h", got)
	}
}

// TestMaxAttemptCredentialLifetimeEnvBinds: the operator can shorten the ceiling
// via LEOFLOW_AUTH_MAX_ATTEMPT_CREDENTIAL_LIFETIME as a duration string.
func TestMaxAttemptCredentialLifetimeEnvBinds(t *testing.T) {
	t.Setenv("LEOFLOW_AUTH_MAX_ATTEMPT_CREDENTIAL_LIFETIME", "90m")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if got := c.Auth.MaxAttemptCredentialLifetime; got != 90*time.Minute {
		t.Errorf("auth.max_attempt_credential_lifetime = %v, want 90m", got)
	}
}
