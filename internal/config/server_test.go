package config

import (
	"os"
	"path/filepath"
	"strings"
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

// TestServerConfigValidateProviderAllowlist locks the auth.provider allowlist so
// an unimplemented or misspelled provider fails closed at boot rather than
// silently falling back to the JWT authenticator main.go always builds.
func TestServerConfigValidateProviderAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		secret   string
		wantErr  bool
	}{
		{"unset defaults to jwt", "", "set", false},
		{"jwt with secret", "jwt", "set", false},
		{"jwt without secret", "jwt", "", true},
		{"oidc known but unimplemented", "oidc", "set", true},
		{"garbage unknown provider", "garbage", "set", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Auth.Provider = tc.provider
			c.Auth.JWT.Secret = tc.secret
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("provider %q secret %q: Validate() = nil, want error", tc.provider, tc.secret)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("provider %q secret %q: Validate() = %v, want nil", tc.provider, tc.secret, err)
			}
		})
	}
}

// TestServerConfigValidateOIDCMessage locks the actionable hint for the known
// but unimplemented oidc provider, so an operator is told to switch to jwt
// rather than left guessing why boot failed.
func TestServerConfigValidateOIDCMessage(t *testing.T) {
	c := &ServerConfig{}
	c.Auth.Provider = "oidc"
	c.Auth.JWT.Secret = "set"
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for oidc provider, want error")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("oidc error = %q, want it to mention it is not yet implemented", err.Error())
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

// TestLoadServerReadsUIAutoRefreshIntervalFromEnv pins the bug that broke #247:
// `leoflow lite` exports LEOFLOW_UI_AUTO_REFRESH_INTERVAL_SECONDS=1 so the SPA
// polls fast in the dev loop, but the server returned 30 (the handler fallback)
// because `ui.auto_refresh_interval_seconds` was missing from serverDefaults —
// without an entry there, viper's AutomaticEnv never bound the env key, so the
// value silently fell back to the zero default. Users had to reload the page
// to see state changes (observed locally 2026-06-01).
func TestLoadServerReadsUIAutoRefreshIntervalFromEnv(t *testing.T) {
	t.Setenv("LEOFLOW_UI_AUTO_REFRESH_INTERVAL_SECONDS", "1")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.UI.AutoRefreshIntervalSeconds != 1 {
		t.Errorf("UI.AutoRefreshIntervalSeconds = %d, want 1 (env var must override default)", c.UI.AutoRefreshIntervalSeconds)
	}
}
