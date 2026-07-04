package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLiteDbtBin: a Lite compile parses the manifest with the per-DAG venv's dbt
// (~/.leoflow/dev/venvs/<dag>/bin/dbt) — the same dbt the task runs — not a system
// dbt the user may not have (L1). It returns "" when the venv dbt is absent so the
// caller falls back to PATH.
func TestLiteDbtBin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := liteDbtBinAt(home, "sales"); got != "" {
		t.Errorf("no venv yet: want empty, got %q", got)
	}

	bin := filepath.Join(home, ".leoflow", "dev", "venvs", "sales", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	dbtPath := filepath.Join(bin, "dbt")
	if err := os.WriteFile(dbtPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := liteDbtBinAt(home, "sales"); got != dbtPath {
		t.Errorf("with venv dbt: got %q, want %q", got, dbtPath)
	}
	if got := liteDbtBinAt(home, ""); got != "" {
		t.Errorf("empty dag id: want empty, got %q", got)
	}
}

// TestDbtProjectHasProfiles gates the L4 default duckdb: a project that ships its own
// profiles.yml must report true so the compiler never overrides the user's warehouse.
func TestDbtProjectHasProfiles(t *testing.T) {
	dir := t.TempDir()
	if dbtProjectHasProfiles(dir) {
		t.Error("no profiles.yml: want false")
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles.yml"), []byte("x: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !dbtProjectHasProfiles(dir) {
		t.Error("with profiles.yml: want true (L4 must not override a user-configured warehouse)")
	}
}
