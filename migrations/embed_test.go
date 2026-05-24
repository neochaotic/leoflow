package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedMigrationsPresent(t *testing.T) {
	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		t.Fatalf("reading embedded migrations: %v", err)
	}
	var up int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			up++
		}
	}
	if up == 0 {
		t.Fatal("no .up.sql migrations embedded")
	}
	// The first migration must be present (sanity that the glob caught real files).
	if _, err := fs.ReadFile(Files, "001_init_tenants_and_rbac.up.sql"); err != nil {
		t.Errorf("expected 001_init...up.sql embedded: %v", err)
	}
	t.Logf("embedded %d up-migrations", up)
}
