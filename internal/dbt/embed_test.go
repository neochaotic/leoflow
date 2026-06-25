package dbt

import (
	"reflect"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

// EmbedGroup namespaces a rendered dbt group's tasks and wires its external edges:
// upstream task_ids become dependencies of the group's roots, and it returns the
// namespaced leaf ids so the caller can wire them into downstream tasks.
func TestEmbedGroupNamespacesAndWires(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "raw", Type: domain.TaskTypeBash, Entrypoint: "dbt seed --select raw"},
		{TaskID: "stg", Type: domain.TaskTypeBash, Entrypoint: "dbt run --select stg", DependsOn: []string{"raw"}},
		{TaskID: "mart", Type: domain.TaskTypeBash, Entrypoint: "dbt run --select mart", DependsOn: []string{"stg"}},
	}

	embedded, leaves, err := EmbedGroup("analytics", tasks, []string{"extract"})
	if err != nil {
		t.Fatalf("EmbedGroup() error: %v", err)
	}
	byID := tasksByID(embedded)

	for _, id := range []string{"analytics__raw", "analytics__stg", "analytics__mart"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing namespaced task %q (have %v)", id, ids(byID))
		}
	}
	// a root inherits the external upstream
	if !reflect.DeepEqual(byID["analytics__raw"].DependsOn, []string{"extract"}) {
		t.Errorf("root deps = %v, want [extract]", byID["analytics__raw"].DependsOn)
	}
	// internal edges are namespaced
	if !reflect.DeepEqual(byID["analytics__stg"].DependsOn, []string{"analytics__raw"}) {
		t.Errorf("stg deps = %v, want [analytics__raw]", byID["analytics__stg"].DependsOn)
	}
	// the leaf is reported for downstream wiring
	if !reflect.DeepEqual(leaves, []string{"analytics__mart"}) {
		t.Errorf("leaves = %v, want [analytics__mart]", leaves)
	}
}

// An empty group name is a loud error (a group must be identifiable).
func TestEmbedGroupRejectsEmptyName(t *testing.T) {
	if _, _, err := EmbedGroup("", []domain.TaskSpec{{TaskID: "x"}}, nil); err == nil {
		t.Fatal("expected an error for an empty group name")
	}
}
