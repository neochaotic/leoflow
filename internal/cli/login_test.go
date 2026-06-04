package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/config"
)

func TestLoginPersistsTokenAndServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/token" {
			t.Errorf("login posted to %q, want /auth/token", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"access_token":"jwt-login"}`)
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, _, err := run(t, "login", "--server", srv.URL,
		"--username", "admin", "--password", "pw", "--config", cfgPath)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "logged in") {
		t.Errorf("output = %q, want a 'logged in' confirmation", out)
	}

	cfg, err := config.Load(cfgPath, nil)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Token != "jwt-login" {
		t.Errorf("persisted Token = %q, want jwt-login", cfg.Token)
	}
	if cfg.ServerURL != srv.URL {
		t.Errorf("persisted ServerURL = %q, want %q", cfg.ServerURL, srv.URL)
	}
}

func TestLoginUsesConfigServerWhenFlagOmitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"jwt-from-config"}`)
	}))
	defer srv.Close()

	// Seed a config file whose server_url points at the fake server; login must
	// fall back to it when --server is not given.
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server_url: "+srv.URL+"\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, _, err := run(t, "login", "--username", "a", "--password", "b", "--config", cfgPath); err != nil {
		t.Fatalf("login with server from config: %v", err)
	}
	cfg, err := config.Load(cfgPath, nil)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Token != "jwt-from-config" {
		t.Errorf("Token = %q, want jwt-from-config", cfg.Token)
	}
}

func TestLoginFailsLoudlyOnBadCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if _, _, err := run(t, "login", "--server", srv.URL,
		"--username", "admin", "--password", "wrong", "--config", cfgPath); err == nil {
		t.Error("expected login to fail on 401, got nil error")
	}
}
