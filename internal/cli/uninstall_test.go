package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestRemoveBinariesIn covers the uninstall fix: the leoflow binaries (which
// install.sh puts on a PATH dir like /usr/local/bin, not under ~/.leoflow) must be
// removed, while unrelated files in the same dir are left untouched.
func TestRemoveBinariesIn(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"leoflow", "leoflow-server", "leoflow-agent", "other-tool"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removeBinariesIn(&bytes.Buffer{}, dir)
	for _, n := range []string{"leoflow", "leoflow-server", "leoflow-agent"} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", n)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "other-tool")); err != nil {
		t.Error("unrelated binaries must be left untouched")
	}
	removeBinariesIn(&bytes.Buffer{}, "") // empty dir is a no-op, must not panic
}

// TestResolveLiteProjectRejectsNonProjectArg covers the CLI clarity fix: an
// explicit `leoflow lite <arg>` that is not a project (e.g. the `leoflow lite
// uninstall` typo) fails with an actionable message, not a cryptic leoflow.yaml error.
func TestResolveLiteProjectRejectsNonProjectArg(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)

	if _, err := resolveLiteProject(cmd, []string{"uninstall"}); err == nil {
		t.Fatal("a non-project argument should error")
	} else if !strings.Contains(err.Error(), "no Leoflow project") || !strings.Contains(err.Error(), "leoflow uninstall") {
		t.Errorf("error should be actionable, got: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leoflow.yaml"), []byte("dag_id: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveLiteProject(cmd, []string{dir}); err != nil || got != dir {
		t.Errorf("existing project: got (%q, %v), want (%q, nil)", got, err, dir)
	}
}

// uninstallCmd builds a command with the given stdin and captured output.
func uninstallCmd(stdin string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetIn(strings.NewReader(stdin))
	return cmd, out
}

func seedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".leoflow", "bin")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "leoflow"), []byte("bin"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestUninstallRemovesHomeWithYes(t *testing.T) {
	home := seedHome(t)
	cmd, _ := uninstallCmd("")
	if err := runUninstall(cmd, true, false); err != nil {
		t.Fatalf("uninstall --yes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".leoflow")); !os.IsNotExist(err) {
		t.Errorf("~/.leoflow should be removed, err=%v", err)
	}
}

func TestUninstallRemovesDatastoreByDefault(t *testing.T) {
	seedHome(t)
	cmd, out := uninstallCmd("no\n") // decline, just to capture the announced plan
	if err := runUninstall(cmd, false, false); err != nil {
		t.Fatalf("uninstall (no --purge): %v", err)
	}
	// Default uninstall drops this user's datastore volumes so a reinstall starts
	// fresh (the stale-admin / old-runs trap); the workspace is kept unless --purge.
	if !strings.Contains(out.String(), "datastore") {
		t.Errorf("default uninstall must announce removing the datastore volumes; got:\n%s", out.String())
	}
}

func TestUninstallAbortsWithoutConfirmation(t *testing.T) {
	home := seedHome(t)
	cmd, out := uninstallCmd("no\n") // user declines
	if err := runUninstall(cmd, false, false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".leoflow")); err != nil {
		t.Errorf("~/.leoflow must NOT be removed when the user declines, err=%v", err)
	}
	if !strings.Contains(out.String(), "aborted") {
		t.Errorf("should report aborted, got %q", out.String())
	}
}

func TestUninstallConfirmedByTypingYes(t *testing.T) {
	home := seedHome(t)
	cmd, _ := uninstallCmd("yes\n")
	if err := runUninstall(cmd, false, false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".leoflow")); !os.IsNotExist(err) {
		t.Errorf("typing 'yes' should remove ~/.leoflow, err=%v", err)
	}
}

func TestUninstallNothingToRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.leoflow
	cmd, out := uninstallCmd("")
	if err := runUninstall(cmd, true, false); err != nil {
		t.Fatalf("uninstall on a clean home: %v", err)
	}
	if !strings.Contains(out.String(), "Nothing to remove") {
		t.Errorf("should report nothing to remove, got %q", out.String())
	}
}

func TestUninstallWired(t *testing.T) {
	found := false
	for _, c := range NewRootCommand().Commands() {
		if c.Name() == "uninstall" {
			found = true
		}
	}
	if !found {
		t.Error("`leoflow uninstall` should be registered on the root command")
	}
}
