package cli

import "testing"

// TestLitePostgresDefaultIsManaged pins the promoted default: `leoflow lite` uses
// the managed relocatable Postgres (Docker-free datastore) unless --postgres
// docker is passed. Promotion is safe now that managed PG is socket-only (no 5432
// collision) and Lite is Redis-free (ADR 0026).
func TestLitePostgresDefaultIsManaged(t *testing.T) {
	f := newLiteCommand().Flags().Lookup("postgres")
	if f == nil {
		t.Fatal("--postgres flag not defined")
	}
	if f.DefValue != datastoreManaged {
		t.Errorf("--postgres default = %q, want %q", f.DefValue, datastoreManaged)
	}
}
