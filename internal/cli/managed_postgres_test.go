package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestManagedPGPaths pins the per-user managed-Postgres layout the rest of the
// lifecycle depends on: the bin dir and data dir both live under ~/.leoflow so
// root and an unprivileged user never share a cluster.
func TestManagedPGPaths(t *testing.T) {
	binDir, dataDir, err := managedPGPaths()
	if err != nil {
		t.Fatalf("managedPGPaths: %v", err)
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		t.Fatalf("UserHomeDir: %v", herr)
	}
	if want := filepath.Join(home, ".leoflow", "postgres", "bin"); binDir != want {
		t.Errorf("binDir = %q, want %q", binDir, want)
	}
	if want := filepath.Join(home, ".leoflow", "pgdata"); dataDir != want {
		t.Errorf("dataDir = %q, want %q", dataDir, want)
	}
}

// TestDetectLibcReturnsKnownValue asserts libc detection yields a value the
// relocatable-PostgreSQL resolver understands: "" on darwin, glibc/musl on linux.
func TestDetectLibcReturnsKnownValue(t *testing.T) {
	switch got := detectLibc(); got {
	case "", "glibc", "musl":
		// expected
	default:
		t.Errorf("detectLibc() = %q, want one of {\"\", \"glibc\", \"musl\"}", got)
	}
}
