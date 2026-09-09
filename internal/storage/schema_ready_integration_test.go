//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/storage"
)

// TestSchemaReadyAgainstLiveDatabase exercises the readiness schema assertion
// (#1023) against a real Postgres, which is the only place the query itself can
// be wrong: the pure gate is unit-tested, but "does SELECT version, dirty FROM
// schema_migrations actually report an absent table as absent, on this server,
// through this pool" is not something a fake can answer. It is the same
// distinction the issue is about — a Ping proving the connection while saying
// nothing about the schema.
//
// Each case connects with search_path pointed at a scratch schema, so a missing
// or stale schema_migrations is produced WITHOUT touching the migrated one the
// rest of the suite shares.
func TestSchemaReadyAgainstLiveDatabase(t *testing.T) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL must point at a migrated database for the schema readiness test")
	}
	ctx := context.Background()

	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: raw})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pg.Close()

	t.Run("migrated database → ready", func(t *testing.T) {
		if err := pg.SchemaReady(ctx); err != nil {
			t.Errorf("a migrated database must be ready, got: %v", err)
		}
	})

	t.Run("schema_migrations absent → not ready", func(t *testing.T) {
		// The #1023 cluster state: the connection is healthy, the database is empty.
		other := connectWithSearchPath(ctx, t, pg, raw, "leoflow_probe_empty")
		defer other.Close()
		if err := other.Ping(ctx); err != nil {
			t.Fatalf("precondition: the connection itself must be healthy, got: %v", err)
		}
		if err := other.SchemaReady(ctx); err == nil {
			t.Error("a database with no schema_migrations reported ready")
		}
	})

	t.Run("version behind the binary → not ready", func(t *testing.T) {
		// A restore from a backup older than this binary, or a lagging replica.
		other := connectWithSearchPath(ctx, t, pg, raw, "leoflow_probe_behind",
			"CREATE TABLE %[1]s.schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)",
			"INSERT INTO %[1]s.schema_migrations (version, dirty) VALUES (1, false)")
		defer other.Close()
		if err := other.SchemaReady(ctx); err == nil {
			t.Error("a database at v1 reported ready against a binary that requires the embedded latest")
		}
	})

	t.Run("boot and readiness agree", func(t *testing.T) {
		// The point of the fix is that the two verdicts cannot drift: whatever boot
		// refuses to start against, readiness must refuse to be ready against.
		other := connectWithSearchPath(ctx, t, pg, raw, "leoflow_probe_agree")
		defer other.Close()
		bootErr := other.CheckSchemaCurrent(ctx)
		readyErr := other.SchemaReady(ctx)
		if bootErr == nil || readyErr == nil {
			t.Fatalf("both must reject an empty schema: boot=%v readiness=%v", bootErr, readyErr)
		}
		if bootErr.Error() != readyErr.Error() {
			t.Errorf("boot and readiness disagree:\n boot: %v\n ready: %v", bootErr, readyErr)
		}
	})
}

// connectWithSearchPath opens a second pool whose search_path is a scratch schema
// it (re)creates, optionally seeding it, and registers the drop. Unqualified
// lookups then resolve inside that schema, so schema_migrations can be absent or
// stale for this connection while the shared migrated schema is untouched. Each
// seed statement is a format string whose %[1]s is the scratch schema name (the
// seeding connection has its own search_path, so the DDL must qualify itself).
func connectWithSearchPath(ctx context.Context, t *testing.T, admin *storage.Postgres, raw, schema string, seed ...string) *storage.Postgres {
	t.Helper()
	if _, err := admin.Pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatalf("dropping stale scratch schema: %v", err)
	}
	if _, err := admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("creating scratch schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Errorf("dropping scratch schema %s: %v", schema, err)
		}
	})
	for _, stmt := range seed {
		if _, err := admin.Pool.Exec(ctx, fmt.Sprintf(stmt, schema)); err != nil {
			t.Fatalf("seeding scratch schema: %v", err)
		}
	}

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("DATABASE_URL is not a URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: u.String()})
	if err != nil {
		t.Fatalf("connecting with search_path=%s: %v", schema, err)
	}
	return pg
}
