package cli

import (
	"strings"
	"testing"
)

// TestBuildDSNsDockerDefault: with no managed socket dir, the Lite DSNs are the
// Docker datastore's TCP localhost:5432 connections (the default path, unchanged).
func TestBuildDSNsDockerDefault(t *testing.T) {
	d := buildDSNs("")
	if d.database != devDatabaseURL || d.maintenance != devMaintenanceURL || d.migrate != devMigrateURL {
		t.Fatalf("empty socket dir must yield the TCP DSN consts, got %+v", d)
	}
}

// TestBuildDSNsManagedSocket: with a managed socket dir, every DSN connects over
// that Unix socket (host=<dir>) and never over TCP localhost:5432 — so a foreign
// Postgres bound to 5432 can never be reached or mistaken for ours.
func TestBuildDSNsManagedSocket(t *testing.T) {
	dir := "/home/u/.leoflow/pgdata"
	d := buildDSNs(dir)
	for label, dsn := range map[string]string{"database": d.database, "maintenance": d.maintenance, "migrate": d.migrate} {
		if !strings.Contains(dsn, "host="+dir) {
			t.Errorf("%s DSN %q must select the unix socket via host=%s", label, dsn, dir)
		}
		if strings.Contains(dsn, "localhost:5432") || strings.Contains(dsn, "@localhost") {
			t.Errorf("%s DSN %q must not use TCP localhost", label, dsn)
		}
	}
	if !strings.HasPrefix(d.migrate, "pgx5://") {
		t.Errorf("migrate DSN must keep the pgx5 scheme, got %q", d.migrate)
	}
	if !strings.Contains(d.database, "/leoflow_dev") || !strings.Contains(d.maintenance, "/postgres") {
		t.Errorf("DSNs target the wrong databases: %+v", d)
	}
}
