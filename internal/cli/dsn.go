package cli

import (
	"os"
	"path/filepath"
)

// dsnSet holds the three Postgres DSNs Lite needs: the dedicated dev database,
// the maintenance database used only to CREATE it, and the golang-migrate
// (pgx5) variant of the dev database.
type dsnSet struct {
	database    string
	maintenance string
	migrate     string
}

// buildDSNs returns the Lite DSNs for the active datastore. With an empty
// socketDir it returns the Docker datastore's TCP localhost:5432 connections
// (the default). With a managed socketDir it returns Unix-socket DSNs
// (host=<socketDir>, trust auth, no TCP) — so the managed cluster is reached
// only through its own per-user socket and a foreign Postgres bound to 5432 can
// never be connected to or mistaken for ours.
func buildDSNs(socketDir string) dsnSet {
	if socketDir == "" {
		return dsnSet{database: devDatabaseURL, maintenance: devMaintenanceURL, migrate: devMigrateURL}
	}
	q := "?host=" + socketDir + "&sslmode=disable"
	return dsnSet{
		database:    "postgres://leoflow@/" + devDBName + q,
		maintenance: "postgres://leoflow@/postgres" + q,
		migrate:     "pgx5://leoflow@/" + devDBName + q,
	}
}

// devDSNs resolves the Lite DSNs for the current run. It auto-detects a live
// managed cluster by its socket file under ~/.leoflow/pgdata, so every entry
// point (the lite runner, reset-password, db reset) connects consistently
// without threading the --postgres choice through each of them.
func devDSNs() dsnSet {
	return buildDSNs(managedSocketDir())
}

// managedSocketDir returns the managed Postgres data dir when its Unix socket is
// live there (cluster up), or "" otherwise (Docker datastore, or not started).
func managedSocketDir() string {
	_, dataDir, err := managedPGPaths()
	if err != nil {
		return ""
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, ".s.PGSQL.5432")); statErr == nil {
		return dataDir
	}
	return ""
}
