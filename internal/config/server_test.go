package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadServerAppliesDefaults(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	checks := map[string]struct{ got, want any }{
		"http_addr":         {c.Server.HTTPAddr, "0.0.0.0:8080"},
		"metrics_addr":      {c.Server.MetricsAddr, "0.0.0.0:9090"},
		"grpc_addr":         {c.Server.GRPCAddr, "0.0.0.0:9091"},
		"logs.dir":          {c.Logs.Dir, "/var/log/leoflow"},
		"database.url":      {c.Database.URL, "postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable"},
		"max_open_conns":    {c.Database.MaxOpenConns, 25},
		"redis.url":         {c.Redis.URL, ""},
		"auth.provider":     {c.Auth.Provider, "jwt"},
		"token_ttl":         {c.Auth.JWT.TokenTTLSeconds, 3600},
		"loop_interval_ms":  {c.Scheduler.LoopIntervalMS, 1000},
		"scheduler.enabled": {c.Scheduler.Enabled, true},
		// Default is sync passthrough (#127): Pro deployments opt in via values.yaml.
		"scheduler.dispatch.buffer_size": {c.Scheduler.Dispatch.BufferSize, 0},
		"scheduler.dispatch.workers":     {c.Scheduler.Dispatch.Workers, 0},
		"otel.enabled":                   {c.Observability.OTel.Enabled, true},
		"log_level":                      {c.Observability.LogLevel, "info"},
		"log_format":                     {c.Observability.LogFormat, "json"},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", name, c.got, c.want)
		}
	}
}

func TestLoadServerExecutorHTTPDefaults(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.HTTP.InlineMaxDurationSeconds != 300 {
		t.Errorf("inline_max_duration_seconds = %d, want 300", c.Executor.HTTP.InlineMaxDurationSeconds)
	}
	if c.Executor.HTTP.InlineConcurrencyLimit != 256 {
		t.Errorf("inline_concurrency_limit = %d, want 256", c.Executor.HTTP.InlineConcurrencyLimit)
	}
	if c.Executor.HTTP.UserAgent != "leoflow/0.1" {
		t.Errorf("user_agent = %q, want leoflow/0.1", c.Executor.HTTP.UserAgent)
	}
}

func TestLoadServerAgentControlPlaneAddr(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.AgentControlPlaneAddr != "" {
		t.Errorf("default agent_control_plane_addr = %q, want empty (falls back to grpc_addr)", c.Executor.AgentControlPlaneAddr)
	}
	t.Setenv("LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR", "host.k3d.internal:9091")
	c, err = LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.AgentControlPlaneAddr != "host.k3d.internal:9091" {
		t.Errorf("agent_control_plane_addr = %q, want host.k3d.internal:9091", c.Executor.AgentControlPlaneAddr)
	}
}

func TestLoadServerExecutorTypeDefault(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.Type != "kubernetes" {
		t.Errorf("default executor.type = %q, want kubernetes", c.Executor.Type)
	}
	if c.Executor.AgentPath != "leoflow-agent" {
		t.Errorf("default executor.agent_path = %q, want leoflow-agent", c.Executor.AgentPath)
	}
	t.Setenv("LEOFLOW_EXECUTOR_TYPE", "subprocess")
	c, err = LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.Type != "subprocess" {
		t.Errorf("executor.type = %q, want subprocess", c.Executor.Type)
	}
}

func TestLoadServerExecutorHTTPEnvOverride(t *testing.T) {
	t.Setenv("LEOFLOW_EXECUTOR_HTTP_INLINE_MAX_DURATION_SECONDS", "60")
	t.Setenv("LEOFLOW_EXECUTOR_HTTP_INLINE_CONCURRENCY_LIMIT", "16")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.HTTP.InlineMaxDurationSeconds != 60 {
		t.Errorf("inline_max_duration_seconds = %d, want 60", c.Executor.HTTP.InlineMaxDurationSeconds)
	}
	if c.Executor.HTTP.InlineConcurrencyLimit != 16 {
		t.Errorf("inline_concurrency_limit = %d, want 16", c.Executor.HTTP.InlineConcurrencyLimit)
	}
}

func TestLoadServerEnvOverridesNestedKey(t *testing.T) {
	t.Setenv("LEOFLOW_SERVER_HTTP_ADDR", "127.0.0.1:9999")
	t.Setenv("LEOFLOW_AUTH_JWT_SECRET", "s3cr3t")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Server.HTTPAddr != "127.0.0.1:9999" {
		t.Errorf("HTTPAddr = %q, want 127.0.0.1:9999", c.Server.HTTPAddr)
	}
	if c.Auth.JWT.Secret != "s3cr3t" {
		t.Errorf("JWT.Secret = %q, want s3cr3t", c.Auth.JWT.Secret)
	}
}

func TestLoadServerFileOverridesDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.yaml")
	body := "server:\n  http_addr: \"0.0.0.0:7000\"\nauth:\n  jwt:\n    secret: filesecret\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(p, nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Server.HTTPAddr != "0.0.0.0:7000" {
		t.Errorf("HTTPAddr = %q, want 0.0.0.0:7000", c.Server.HTTPAddr)
	}
	if c.Auth.JWT.Secret != "filesecret" {
		t.Errorf("JWT.Secret = %q, want filesecret", c.Auth.JWT.Secret)
	}
}

func TestServerConfigValidateRequiresJWTSecret(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil with empty JWT secret, want error")
	}
	c.Auth.JWT.Secret = "set"
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v with JWT secret set, want nil", err)
	}
}

func TestValidateRejectsDevNoAuthOnNonLoopback(t *testing.T) {
	base := func() *ServerConfig {
		c := &ServerConfig{}
		c.Auth.Provider = "none" // skip the jwt-secret requirement
		c.Auth.DevNoAuth = true
		return c
	}
	// Exposed on all interfaces with auth disabled → must be rejected.
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "192.168.1.10:8080"} {
		c := base()
		c.Server.HTTPAddr = addr
		if err := c.Validate(); err == nil {
			t.Errorf("dev_no_auth on %q must be rejected", addr)
		}
	}
	// Loopback is allowed (the no-auth API is not reachable off-host).
	for _, addr := range []string{"127.0.0.1:8080", "localhost:8080"} {
		c := base()
		c.Server.HTTPAddr = addr
		if err := c.Validate(); err != nil {
			t.Errorf("dev_no_auth on loopback %q should be allowed, got %v", addr, err)
		}
	}
	// dev_no_auth off → any address is fine.
	c := &ServerConfig{}
	c.Server.HTTPAddr = "0.0.0.0:8080"
	if err := c.Validate(); err != nil {
		t.Errorf("non-dev config should validate, got %v", err)
	}
}
