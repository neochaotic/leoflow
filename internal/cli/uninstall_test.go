package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

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
