package cli

import "testing"

// TestLitePostgresDefaultIsDocker pins the default datastore: `leoflow lite` uses
// the Docker postgres:16 (robust wherever Docker runs — and the default executor
// already needs Docker), with the relocatable managed PG available as the
// Docker-free opt-in via --postgres managed.
func TestLitePostgresDefaultIsDocker(t *testing.T) {
	f := newLiteCommand().Flags().Lookup("postgres")
	if f == nil {
		t.Fatal("--postgres flag not defined")
	}
	if f.DefValue != datastoreDocker {
		t.Errorf("--postgres default = %q, want %q", f.DefValue, datastoreDocker)
	}
}
