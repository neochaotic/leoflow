package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
		return fmt.Errorf("%w: schema_migrations is absent — migrations have not run (apply them: the Helm pre-upgrade migration Job, or `leoflow db migrate` for Lite); schema v%d is required",
			errSchemaNotCurrent, latest)
	case dirty:
		return fmt.Errorf("%w: schema is dirty at v%d — a migration did not complete; resolve it before starting",
			errSchemaNotCurrent, dbVersion)
	case dbVersion < latest:
		return fmt.Errorf("%w: schema is at v%d but this binary requires v%d — run the pending migrations (the Helm pre-upgrade migration Job, or `leoflow db migrate` for Lite)",
			errSchemaNotCurrent, dbVersion, latest)
	default:
		// dbVersion >= latest. An AHEAD schema (dbVersion > latest) is NOT fatal:
		// expand-contract migrations are backward-compatible by construction, so
		// older code is expected to run against a newer schema during a rollout or a
		// `helm rollback`. Hard-failing here would CrashLoopBackOff the old pods on
		// rollback and turn a recoverable bad deploy into an outage. The caller logs
		// a warning for the ahead case; boot proceeds.
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
	if exists && !dirty && version > latest {
		// Ahead schema (see checkSchemaCurrent): boot proceeds — expand-contract
		// migrations keep older code working — but surface it, since it means this
		// binary is older than the DB (a rollout in progress or a rollback).
		slog.Warn("database schema is ahead of this binary; proceeding (expand-contract assumed backward-compatible)",
			"db_version", version, "binary_latest", latest)
	}
	return checkSchemaCurrent(version, exists, dirty, latest)
}
