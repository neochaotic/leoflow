package cli

import "testing"

// TestLitePostgresDefaultIsAuto pins the default datastore selection: `leoflow
// lite` resolves the Postgres backend for the host — the Docker postgres:16 when
// Docker is present (the realistic case, since the k3d executor needs Docker
// too), else the managed relocatable PG (Docker-free), with both forceable.
func TestLitePostgresDefaultIsAuto(t *testing.T) {
	f := newLiteCommand().Flags().Lookup("postgres")
	if f == nil {
		t.Fatal("--postgres flag not defined")
	}
	if f.DefValue != datastoreAuto {
		t.Errorf("--postgres default = %q, want %q", f.DefValue, datastoreAuto)
	}
}

// TestResolveDatastore: "auto" picks Docker when Docker is available, else the
// managed relocatable PG; an explicit value is returned unchanged.
func TestResolveDatastore(t *testing.T) {
	cases := []struct {
		flag     string
		dockerOK bool
		want     string
	}{
		{datastoreAuto, true, datastoreDocker},
		{datastoreAuto, false, datastoreManaged},
		{datastoreDocker, false, datastoreDocker},
		{datastoreManaged, true, datastoreManaged},
	}
	for _, c := range cases {
		if got := resolveDatastore(c.flag, c.dockerOK); got != c.want {
			t.Errorf("resolveDatastore(%q, dockerOK=%v) = %q, want %q", c.flag, c.dockerOK, got, c.want)
		}
	}
}
