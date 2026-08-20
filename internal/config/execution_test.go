package config

import (
	"strings"
	"testing"
	"time"
)

// TestExecutionDefaults locks the byte-for-byte no-op defaults (ADR 0058 N1a): a
// fresh config keeps warm pools OFF and carries the D6/D9 caps at their documented
// values, so a default deploy behaves exactly like today's pod-per-task.
func TestExecutionDefaults(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Execution.WarmPoolsEnabled {
		t.Errorf("execution.warm_pools_enabled default = true, want false")
	}
	if got := c.Execution.MaxAttemptsPerWorker; got != 50 {
		t.Errorf("execution.max_attempts_per_worker default = %d, want 50", got)
	}
	if got := c.Execution.MaxWorkerLifetime; got != time.Hour {
		t.Errorf("execution.max_worker_lifetime default = %v, want 1h", got)
	}
	if got := c.Execution.MinIdleWorkers; got != 0 {
		t.Errorf("execution.min_idle_workers default = %d, want 0", got)
	}
	if got := c.Execution.WorkerIdleTTL; got != 5*time.Minute {
		t.Errorf("execution.worker_idle_ttl default = %v, want 5m", got)
	}
}

// TestExecutionEnvBinds proves each execution key binds from its LEOFLOW_* env var
// (they are registered in serverDefaults so AutomaticEnv sees them).
func TestExecutionEnvBinds(t *testing.T) {
	t.Setenv("LEOFLOW_EXECUTION_WARM_POOLS_ENABLED", "true")
	t.Setenv("LEOFLOW_EXECUTION_MAX_ATTEMPTS_PER_WORKER", "10")
	t.Setenv("LEOFLOW_EXECUTION_MAX_WORKER_LIFETIME", "2h")
	t.Setenv("LEOFLOW_EXECUTION_MIN_IDLE_WORKERS", "3")
	t.Setenv("LEOFLOW_EXECUTION_WORKER_IDLE_TTL", "90s")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if !c.Execution.WarmPoolsEnabled {
		t.Errorf("execution.warm_pools_enabled = false, want true (from env)")
	}
	if got := c.Execution.MaxAttemptsPerWorker; got != 10 {
		t.Errorf("execution.max_attempts_per_worker = %d, want 10 (from env)", got)
	}
	if got := c.Execution.MaxWorkerLifetime; got != 2*time.Hour {
		t.Errorf("execution.max_worker_lifetime = %v, want 2h (from env)", got)
	}
	if got := c.Execution.MinIdleWorkers; got != 3 {
		t.Errorf("execution.min_idle_workers = %d, want 3 (from env)", got)
	}
	if got := c.Execution.WorkerIdleTTL; got != 90*time.Second {
		t.Errorf("execution.worker_idle_ttl = %v, want 90s (from env)", got)
	}
}

// validWarmConfig returns a ServerConfig that passes the warm-pool boot gate: warm
// pools on, token-exchange transport, liveness enforce, a worker lifetime >= the
// attempt-credential ceiling, and sane caps.
func validWarmConfig() *ServerConfig {
	c := &ServerConfig{}
	c.Auth.JWT.Secret = "set"
	c.Auth.AgentTokenTransport = AgentTokenTransportExchange
	c.Auth.SecretLivenessMode = SecretLivenessEnforce
	c.Auth.MaxAttemptCredentialLifetime = 30 * time.Minute
	c.Execution.WarmPoolsEnabled = true
	c.Execution.MaxAttemptsPerWorker = 50
	c.Execution.MaxWorkerLifetime = time.Hour
	c.Execution.WorkerIdleTTL = 5 * time.Minute
	return c
}

// TestValidateExecutionWarmPoolsOffIgnoresCoupling locks that when warm pools are
// OFF (the default), Validate() succeeds regardless of transport/liveness/caps: an
// operator who never turns warm pools on is completely unaffected.
func TestValidateExecutionWarmPoolsOffIgnoresCoupling(t *testing.T) {
	c := &ServerConfig{}
	c.Auth.JWT.Secret = "set"
	// Defaults: warm pools off, envvar transport, observe liveness, zero caps.
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v with warm pools off, want nil", err)
	}
	// Even with zero caps and the insecure transport/liveness, off = no coupling.
	c.Auth.AgentTokenTransport = AgentTokenTransportEnvVar
	c.Auth.SecretLivenessMode = SecretLivenessObserve
	c.Execution.MaxAttemptsPerWorker = 0
	c.Execution.MaxWorkerLifetime = 0
	c.Execution.WorkerIdleTTL = 0
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v with warm pools off and zero caps, want nil", err)
	}
}

// TestValidateExecutionWarmPoolsOnSucceeds locks the happy path: warm pools on with
// the security prerequisites and a compatible lifetime ordering boots.
func TestValidateExecutionWarmPoolsOnSucceeds(t *testing.T) {
	if err := validWarmConfig().Validate(); err != nil {
		t.Errorf("Validate() = %v for a valid warm-pool config, want nil", err)
	}
}

// TestValidateExecutionWarmPoolsRequiresTransportExchange locks HIGH #1: warm pools
// on with the default envvar transport fails boot closed and names the required
// setting.
func TestValidateExecutionWarmPoolsRequiresTransportExchange(t *testing.T) {
	c := validWarmConfig()
	c.Auth.AgentTokenTransport = AgentTokenTransportEnvVar
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for warm pools on with envvar transport, want error")
	}
	if !strings.Contains(err.Error(), "agent_token_transport") {
		t.Errorf("error = %q, want it to name auth.agent_token_transport", err.Error())
	}
}

// TestValidateExecutionWarmPoolsRequiresLivenessEnforce locks HIGH #1: warm pools on
// with the default observe liveness fails boot closed and names the required setting.
func TestValidateExecutionWarmPoolsRequiresLivenessEnforce(t *testing.T) {
	c := validWarmConfig()
	c.Auth.SecretLivenessMode = SecretLivenessObserve
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for warm pools on with observe liveness, want error")
	}
	if !strings.Contains(err.Error(), "secret_liveness_mode") {
		t.Errorf("error = %q, want it to name auth.secret_liveness_mode", err.Error())
	}
}

// TestValidateExecutionWorkerLifetimeOrdering locks the D9 guard: a worker lifetime
// shorter than the attempt-credential ceiling fails boot closed, because a worker
// could be force-recycled mid-attempt by token lapse.
func TestValidateExecutionWorkerLifetimeOrdering(t *testing.T) {
	c := validWarmConfig()
	c.Auth.MaxAttemptCredentialLifetime = 24 * time.Hour
	c.Execution.MaxWorkerLifetime = 10 * time.Minute
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for worker lifetime < credential lifetime, want error")
	}
	if !strings.Contains(err.Error(), "max_worker_lifetime") {
		t.Errorf("error = %q, want it to name execution.max_worker_lifetime", err.Error())
	}
}

// TestValidateExecutionSanityCaps locks the D9 sanity caps (only enforced when warm
// pools are on): a zero/negative attempts cap, worker lifetime, or idle TTL fails
// boot closed rather than recycling instantly or never.
func TestValidateExecutionSanityCaps(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*ServerConfig)
		want string
	}{
		{"zero attempts", func(c *ServerConfig) { c.Execution.MaxAttemptsPerWorker = 0 }, "max_attempts_per_worker"},
		{"negative attempts", func(c *ServerConfig) { c.Execution.MaxAttemptsPerWorker = -1 }, "max_attempts_per_worker"},
		{"zero worker lifetime", func(c *ServerConfig) {
			c.Auth.MaxAttemptCredentialLifetime = 0
			c.Execution.MaxWorkerLifetime = 0
		}, "max_worker_lifetime"},
		{"zero idle ttl", func(c *ServerConfig) { c.Execution.WorkerIdleTTL = 0 }, "worker_idle_ttl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validWarmConfig()
			tc.mut(c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil with %s, want error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err.Error(), tc.want)
			}
		})
	}
}
