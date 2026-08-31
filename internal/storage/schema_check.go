package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/neochaotic/leoflow/migrations"
)

// errSchemaNotCurrent is the sentinel for a boot-time schema mismatch, so callers
// can recognize it distinctly from a transient DB error.
var errSchemaNotCurrent = errors.New("database schema is not current")

// SchemaVersion reads the applied migration version from golang-migrate's
// schema_migrations table. exists is false when the table is absent (migrations
// never ran). It issues only a SELECT, so it is compatible with the read-only
// role:api DB identity (ADR 0049), which skips migrations by design.
func (p *Postgres) SchemaVersion(ctx context.Context) (version uint, dirty, exists bool, err error) {
	var v int64
	var d bool
	row := p.Pool.QueryRow(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1")
	if scanErr := row.Scan(&v, &d); scanErr != nil {
		// An absent table (42P01 undefined_table) means migrations never ran — a
		// legible "exists=false", not a hard error. Anything else is a real fault.
		var pgErr *pgconn.PgError
		if errors.As(scanErr, &pgErr) && pgErr.Code == "42P01" {
			return 0, false, false, nil
		}
		return 0, false, false, scanErr
	}
	if v < 0 {
		return 0, false, false, fmt.Errorf("schema_migrations.version is negative (%d)", v)
	}
	// #nosec G115 -- a migration version is a small, non-negative counter.
	return uint(v), d, true, nil
}

// checkSchemaCurrent fails fast when the database schema does not match the
// binary's embedded latest migration, with an actionable message — instead of
// booting and erroring at the first query against a missing/old table.
func checkSchemaCurrent(dbVersion uint, exists, dirty bool, latest uint) error {
	switch {
	case !exists:
		return fmt.Errorf("%w: schema_migrations is absent — migrations have not run (apply them: the Helm migration Job, or `leoflow migrate`); schema v%d is required",
			errSchemaNotCurrent, latest)
	case dirty:
		return fmt.Errorf("%w: schema is dirty at v%d — a migration did not complete; resolve it before starting",
			errSchemaNotCurrent, dbVersion)
	case dbVersion < latest:
		return fmt.Errorf("%w: schema is at v%d but this binary requires v%d — run the pending migrations (the Helm migration Job, or `leoflow migrate`)",
			errSchemaNotCurrent, dbVersion, latest)
	case dbVersion > latest:
		return fmt.Errorf("%w: schema is at v%d, ahead of this binary's v%d — this server is older than the database; deploy a matching or newer build",
			errSchemaNotCurrent, dbVersion, latest)
	default:
		return nil
	}
}

// CheckSchemaCurrent reads the DB schema version and compares it to the binary's
// embedded latest, failing fast on a mismatch. Called at boot after the pool is
// open, before the server serves.
func (p *Postgres) CheckSchemaCurrent(ctx context.Context) error {
	latest, err := migrations.Latest()
	if err != nil {
		return fmt.Errorf("reading embedded schema version: %w", err)
	}
	version, dirty, exists, err := p.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("reading database schema version: %w", err)
	}
	return checkSchemaCurrent(version, exists, dirty, latest)
}
