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
		"database.url":      {c.Database.URL, "postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable"},
		"max_open_conns":    {c.Database.MaxOpenConns, 25},
		"redis.url":         {c.Redis.URL, "redis://localhost:6379/0"},
		"auth.provider":     {c.Auth.Provider, "jwt"},
		"token_ttl":         {c.Auth.JWT.TokenTTLSeconds, 3600},
		"loop_interval_ms":  {c.Scheduler.LoopIntervalMS, 1000},
		"scheduler.enabled": {c.Scheduler.Enabled, true},
		"otel.enabled":      {c.Observability.OTel.Enabled, true},
		"log_level":         {c.Observability.LogLevel, "info"},
		"log_format":        {c.Observability.LogFormat, "json"},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", name, c.got, c.want)
		}
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
