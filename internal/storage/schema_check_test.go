package storage

import (
	"errors"
	"testing"
)

// checkSchemaCurrent is the pure boot-time gate: given the DB's schema state and
// the binary's embedded latest version, it fails fast with an actionable message
// rather than letting the server boot and error at the first query that touches a
// missing/old table (ADR 0060 S1). It runs even under role:api (a read-only
// SELECT), which skips migrations by design (ADR 0049).
func TestCheckSchemaCurrent(t *testing.T) {
	const latest = 26

	t.Run("current + clean = ok", func(t *testing.T) {
		if err := checkSchemaCurrent(latest, true, false, latest); err != nil {
			t.Errorf("current schema returned error: %v", err)
		}
	})

	t.Run("table missing = fail fast", func(t *testing.T) {
		err := checkSchemaCurrent(0, false, false, latest)
		if err == nil {
			t.Fatal("missing schema_migrations must fail")
		}
	})

	t.Run("behind = fail with the version gap", func(t *testing.T) {
		err := checkSchemaCurrent(latest-3, true, false, latest)
		if err == nil {
			t.Fatal("a DB behind the binary must fail")
		}
	})

	t.Run("ahead = fail (binary older than DB)", func(t *testing.T) {
		err := checkSchemaCurrent(latest+1, true, false, latest)
		if err == nil {
			t.Fatal("a DB ahead of the binary must fail")
		}
	})

	t.Run("dirty = fail (a migration did not complete)", func(t *testing.T) {
		err := checkSchemaCurrent(latest, true, true, latest)
		if err == nil {
			t.Fatal("a dirty schema must fail")
		}
	})

	t.Run("errors are ErrValidation-wrapped for a clean boot message", func(t *testing.T) {
		if err := checkSchemaCurrent(0, false, false, latest); !errors.Is(err, errSchemaNotCurrent) {
			t.Errorf("want errSchemaNotCurrent, got %v", err)
		}
	})
}
