package storage_test

import (
	"os"
	"strings"
	"testing"
)

// TestRoleLadderMigrationDoesNotAssignRoles guards the upgrade-safety invariant
// that the role-ladder migration ships its roles UNASSIGNED: it may only seed
// permissions, roles, and role_permissions — never user_roles — so no
// pre-existing account (the bootstrap admin above all) silently gains access on
// upgrade. This is a property of the migration SQL, asserted statically because
// a shared integration DB cannot: application code (OIDC login reconciliation,
// admin grants) legitimately writes user_roles at runtime. If a future edit adds
// an `INSERT INTO user_roles` to this migration, this test fails.
func TestRoleLadderMigrationDoesNotAssignRoles(t *testing.T) {
	const path = "../../migrations/025_role_ladder.up.sql"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	// Strip `--` line comments before checking, so a comment that merely mentions
	// user_roles (e.g. "ships UNASSIGNED, no user_roles rows") is not a false
	// positive — only executable SQL touching the table must fail the guard.
	var code strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		code.WriteString(strings.ToLower(line))
		code.WriteByte('\n')
	}
	if strings.Contains(code.String(), "user_roles") {
		t.Errorf("%s must not touch user_roles — the ladder ships unassigned so upgrades grant no pre-existing account access", path)
	}
}
