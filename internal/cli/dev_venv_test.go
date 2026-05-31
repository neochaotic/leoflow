package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRuntimeFixture builds a fake pysrc-style runtime tree under dir with the
// given file→content map; subdirs are created as needed. Returns the runtime root.
func writeRuntimeFixture(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	root := filepath.Join(dir, "runtime", "python", "leoflow_runtime")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "runtime", "python")
}

// TestRuntimeSrcChecksum_StableAcrossWalkOrder: the walk MUST be deterministic
// across runs and OSes — the marker check compares hex strings byte-for-byte, so
// any nondeterminism would force gratuitous reinstalls (or skip needed ones).
func TestRuntimeSrcChecksum_StableAcrossWalkOrder(t *testing.T) {
	src := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"__init__.py": "x = 1\n",
		"runner.py":   "def run(): pass\n",
		"xcom.py":     "def pull(): return None\n",
	})
	a, err := runtimeSrcChecksum(src)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	b, err := runtimeSrcChecksum(src)
	if err != nil {
		t.Fatalf("checksum 2nd call: %v", err)
	}
	if a != b {
		t.Errorf("checksum must be stable across calls: %q != %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("expected a hex SHA-256 (64 chars), got %d chars: %q", len(a), a)
	}
}

// TestRuntimeSrcChecksum_ChangesWithContent: editing ANY .py file inside the
// runtime must change the checksum so the venv reinstalls — this is the core
// invariant Lima bug #239 needs (binary upgrade ships a new runner.py with
// lifecycle log lines; the venv must pick it up).
func TestRuntimeSrcChecksum_ChangesWithContent(t *testing.T) {
	v1 := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"runner.py": "def run(): pass\n",
	})
	v2 := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"runner.py": "def run(): print('[leoflow] loading')\n", // simulated upgrade
	})
	a, _ := runtimeSrcChecksum(v1)
	b, _ := runtimeSrcChecksum(v2)
	if a == b {
		t.Errorf("checksum did not change for different content: both %q", a)
	}
}

// TestRuntimeSrcChecksum_IgnoresNonPyFiles: __pycache__, .pyc, README, etc.
// must NOT affect the checksum. Otherwise a stray cache dir would force constant
// reinstalls.
func TestRuntimeSrcChecksum_IgnoresNonPyFiles(t *testing.T) {
	src := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"runner.py": "def run(): pass\n",
	})
	base, _ := runtimeSrcChecksum(src)
	// Drop a stray .pyc and README alongside.
	if err := os.WriteFile(filepath.Join(src, "leoflow_runtime", "runner.cpython-311.pyc"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "leoflow_runtime", "README.md"), []byte("docs"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, _ := runtimeSrcChecksum(src)
	if base != after {
		t.Errorf("stray non-py files must not affect the checksum: %q != %q", base, after)
	}
}

// TestRuntimeMarkerRoundtrip: after writeRuntimeMarker, runtimeInstalledChecksum
// returns the same value, and is empty when no marker exists yet.
func TestRuntimeMarkerRoundtrip(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "venv"), 0o750); err != nil {
		t.Fatal(err)
	}
	if got := runtimeInstalledChecksum(home); got != "" {
		t.Errorf("absent marker must report empty, got %q", got)
	}
	want := "deadbeef" + "00000000000000000000000000000000000000000000000000000000"
	if err := writeRuntimeMarker(home, want); err != nil {
		t.Fatal(err)
	}
	if got := runtimeInstalledChecksum(home); got != want {
		t.Errorf("roundtrip mismatch: got %q want %q", got, want)
	}
}

// TestNeedsRuntimeInstall_BinaryUpgradeTriggersReinstall is the Lima bug #239
// case end-to-end: the venv exists with leoflow_runtime importable, but the
// installed checksum is from a previous binary. Must report needsInstall=true
// even though the import would succeed.
func TestNeedsRuntimeInstall_BinaryUpgradeTriggersReinstall(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "venv"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Marker says checksum X is installed (the old binary's runtime).
	if err := writeRuntimeMarker(home, "deadbeef"+strings.Repeat("0", 56)); err != nil {
		t.Fatal(err)
	}
	// pysrc has a different checksum (the upgraded binary's runtime).
	src := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"runner.py": "def run(): print('[leoflow] new')\n",
	})

	need, want, err := needsRuntimeInstall(home, src, true /*importOK*/)
	if err != nil {
		t.Fatalf("needsRuntimeInstall: %v", err)
	}
	if !need {
		t.Errorf("bug #239: needsRuntimeInstall=false on a mismatched marker; install must fire on binary upgrade")
	}
	if want == "" {
		t.Errorf("expected a non-empty want-checksum to write into the marker after install")
	}
}

// TestNeedsRuntimeInstall_MarkerMatchesIsSkipped: when the installed checksum
// matches the pysrc checksum, the install MUST be skipped — that's the whole
// point of the gate (no needless `pip install` on every lite startup).
func TestNeedsRuntimeInstall_MarkerMatchesIsSkipped(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "venv"), 0o750); err != nil {
		t.Fatal(err)
	}
	src := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"runner.py": "def run(): pass\n",
	})
	want, err := runtimeSrcChecksum(src)
	if err != nil {
		t.Fatal(err)
	}
	if werr := writeRuntimeMarker(home, want); werr != nil {
		t.Fatal(werr)
	}

	need, _, err := needsRuntimeInstall(home, src, true)
	if err != nil {
		t.Fatal(err)
	}
	if need {
		t.Errorf("matching marker must skip install (no needless pip on every startup)")
	}
}

// TestNeedsRuntimeInstall_ImportFailedAlwaysInstalls: when the import check
// failed (fresh venv), the marker is irrelevant — must install.
func TestNeedsRuntimeInstall_ImportFailedAlwaysInstalls(t *testing.T) {
	home := t.TempDir()
	src := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"runner.py": "def run(): pass\n",
	})
	need, _, err := needsRuntimeInstall(home, src, false /*importOK*/)
	if err != nil {
		t.Fatal(err)
	}
	if !need {
		t.Errorf("import check failed must always require install (fresh venv path)")
	}
}

// TestNeedsRuntimeInstall_MissingMarkerInstalls: an old venv that pre-dates the
// marker (installed by a binary that didn't have this check) must reinstall on
// next lite startup — otherwise we never reach a known-good state.
func TestNeedsRuntimeInstall_MissingMarkerInstalls(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "venv"), 0o750); err != nil {
		t.Fatal(err)
	}
	src := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"runner.py": "def run(): pass\n",
	})
	need, _, err := needsRuntimeInstall(home, src, true /*importOK*/)
	if err != nil {
		t.Fatal(err)
	}
	if !need {
		t.Errorf("missing marker on existing venv must trigger reinstall (defensive — recover to known-good)")
	}
}

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
