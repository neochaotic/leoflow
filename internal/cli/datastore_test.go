package cli

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectNameStableAndDistinct: the project name is a pure function of the
// install path — identical on every call (so a reinstall at the same HOME
// reconnects to the same volume) and distinct per path (so two users never share
// or clobber a datastore).
func TestProjectNameStableAndDistinct(t *testing.T) {
	a1 := projectName("/home/alice/.leoflow")
	a2 := projectName("/home/alice/.leoflow")
	b := projectName("/home/bob/.leoflow")
	if a1 != a2 {
		t.Errorf("project name must be deterministic: %q != %q", a1, a2)
	}
	if a1 == b {
		t.Errorf("different install paths must yield different project names, both %q", a1)
	}
	if !strings.HasPrefix(a1, "leoflow-") || len(a1) != len("leoflow-")+12 {
		t.Errorf("project name %q must be leoflow-<12 hex>", a1)
	}
	validProjectChar := func(r rune) bool {
		return r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
	}
	for _, r := range a1 {
		if !validProjectChar(r) {
			t.Errorf("project name %q must be a valid docker compose project (lowercase/digits/-)", a1)
		}
	}
}

// TestResolveDevDBPortPersistsAndReuses: the first call picks a port and persists
// it; later calls reuse the persisted port even if pick would choose another (the
// running container holds it) — so separate processes agree.
func TestResolveDevDBPortPersistsAndReuses(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveDevDBPort(dir, func() int { return 5455 })
	if err != nil || got != 5455 {
		t.Fatalf("first resolve = (%d, %v), want (5455, nil)", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "db-port")); strings.TrimSpace(string(b)) != "5455" {
		t.Errorf("port not persisted, db-port = %q", string(b))
	}
	again, err := resolveDevDBPort(dir, func() int { return 9999 })
	if err != nil || again != 5455 {
		t.Errorf("second resolve must reuse persisted port, got (%d, %v), want 5455", again, err)
	}
}

// TestDevDBPortDefault: with no persisted port (source checkout / explicit
// --compose), the DSNs fall back to the standard 5432.
func TestDevDBPortDefault(t *testing.T) {
	if p := devDBPort(t.TempDir()); p != defaultDevDBPort {
		t.Errorf("devDBPort with no db-port = %d, want %d", p, defaultDevDBPort)
	}
}

// TestFirstFreePortSkipsBusy: a bound port is skipped and the next free one is
// returned.
func TestFirstFreePortSkipsBusy(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	defer func() { _ = ln.Close() }()
	busy := ln.Addr().(*net.TCPAddr).Port
	got := firstFreePort(busy)
	if got <= busy {
		t.Errorf("firstFreePort(%d) = %d, must skip the bound port", busy, got)
	}
	if !portFree(got) {
		t.Errorf("firstFreePort returned a port that is not free: %d", got)
	}
}
