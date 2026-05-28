package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDevDepsSignatureOrderIndependent: the canonical signature ignores ordering,
// so a leoflow.yaml that just reorders `dependencies:` does not force a needless
// reinstall.
func TestDevDepsSignatureOrderIndependent(t *testing.T) {
	a := devDepsSignature([]string{"requests==2.31.0", "duckdb==1.4.4"})
	b := devDepsSignature([]string{"duckdb==1.4.4", "requests==2.31.0"})
	if a != b {
		t.Errorf("signature must be order-independent: %q != %q", a, b)
	}
	c := devDepsSignature([]string{"requests==2.31.0"})
	if a == c {
		t.Errorf("a different dep set must yield a different signature: both %q", a)
	}
}

// TestDevDepsRoundtrip: after writeDevDepsMarker, devDepsUpToDate is true for the
// same deps and false when the deps change — which is what gates the reinstall
// when switching projects or editing dependencies (#116).
func TestDevDepsRoundtrip(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "venv"), 0o750); err != nil {
		t.Fatal(err)
	}
	if devDepsUpToDate(home, []string{"x"}) {
		t.Errorf("an absent marker must report not up-to-date")
	}
	if err := writeDevDepsMarker(home, []string{"requests==2.31.0", "duckdb==1.4.4"}); err != nil {
		t.Fatal(err)
	}
	if !devDepsUpToDate(home, []string{"duckdb==1.4.4", "requests==2.31.0"}) {
		t.Errorf("same deps (any order) must report up-to-date after a successful install")
	}
	if devDepsUpToDate(home, []string{"requests==2.31.0"}) {
		t.Errorf("a different dep set must report not up-to-date (forces reinstall on project switch)")
	}
}
