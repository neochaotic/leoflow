package cli

import (
	"strings"
	"testing"
)

func TestNewDBCommandHasSubcommands(t *testing.T) {
	db := newDBCommand()
	got := map[string]bool{}
	for _, c := range db.Commands() {
		got[c.Name()] = true
	}
	for _, want := range []string{"migrate", "reset"} {
		if !got[want] {
			t.Errorf("db command missing subcommand %q", want)
		}
	}
}

func TestDBResetRequiresYes(t *testing.T) {
	cmd := devTestCmd()
	cmd.SetArgs([]string{"db", "reset"})
	// Wire the db subtree under a throwaway root so we can execute "db reset".
	cmd.AddCommand(newDBCommand())
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("db reset without --yes should refuse with a --yes hint, got %v", err)
	}
}
