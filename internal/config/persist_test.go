package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistSessionPreservesExistingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("admin_email: admin@leoflow.local\nlite_port: 8080\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := PersistSession(path, "https://pro.example.com", "jwt-token-123"); err != nil {
		t.Fatalf("PersistSession: %v", err)
	}

	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "jwt-token-123" {
		t.Errorf("Token = %q, want jwt-token-123", cfg.Token)
	}
	if cfg.ServerURL != "https://pro.example.com" {
		t.Errorf("ServerURL = %q, want the persisted server", cfg.ServerURL)
	}
	// The pre-existing Lite settings must survive the merge.
	if cfg.AdminEmail != "admin@leoflow.local" {
		t.Errorf("AdminEmail = %q, want it preserved", cfg.AdminEmail)
	}
	if cfg.LitePort != 8080 {
		t.Errorf("LitePort = %d, want it preserved (8080)", cfg.LitePort)
	}
}

func TestPersistSessionEmptyPathErrors(t *testing.T) {
	if err := PersistSession("", "http://x", "tok"); err == nil {
		t.Error("expected an error for an empty config path")
	}
}

func TestPersistSessionRejectsCorruptExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// A YAML scalar where a mapping is expected makes Unmarshal-into-map fail.
	if err := os.WriteFile(path, []byte("just-a-string\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := PersistSession(path, "http://x", "tok"); err == nil {
		t.Error("expected an error when the existing config is not a mapping")
	}
}

func TestPersistSessionCreatesFileAndParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "config.yaml")

	if err := PersistSession(path, "http://localhost:8080", "tok"); err != nil {
		t.Fatalf("PersistSession into a missing dir: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected the config file to be created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config perm = %o, want 0600 (token is a secret)", perm)
	}
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "tok" {
		t.Errorf("Token = %q, want tok", cfg.Token)
	}
}
