package domain

import "testing"

func TestCanonicalHashStableAndDistinct(t *testing.T) {
	spec := func(image string) *DAGSpec {
		return &DAGSpec{
			SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: image,
			Tasks: []TaskSpec{{TaskID: "a", Type: TaskTypePython, Entrypoint: "dag:a"}},
		}
	}
	h1, err := spec("img:v1").CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := spec("img:v1").CanonicalHash()
	if h1 != h2 {
		t.Errorf("identical specs hashed differently: %s vs %s", h1, h2)
	}
	h3, _ := spec("img:v2").CanonicalHash()
	if h1 == h3 {
		t.Error("different specs hashed identically")
	}
	if len(h1) != 64 {
		t.Errorf("sha256 hex should be 64 chars, got %d", len(h1))
	}
}
