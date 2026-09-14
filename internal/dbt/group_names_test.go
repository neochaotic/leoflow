package dbt

import (
	"strings"
	"testing"
)

// TestGroupIDsDoNotDependOnMemberCount pins a decision taken while fixing
// #1114 and worth keeping: a grouped task is NOT renamed after its single
// member.
//
// Naming a one-model level `a` reads better than `level_0` — but it makes the
// task id depend on how many models the group happens to hold, so adding a
// sibling silently renames the task from `a` back to `level_0`. task_id is what
// depends_on, run history, retries and URLs are keyed on, and an ordinary edit
// to a dbt project must not rewrite them. The legibility problem is real and is
// solved by describing a task, not by renaming it — which needs a TaskSpec field
// the schema does not have yet.
func TestGroupIDsDoNotDependOnMemberCount(t *testing.T) {
	one := []byte(`{"nodes":{
	  "model.s.a":{"resource_type":"model","name":"a","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["s","staging","a"]}}}`)
	two := []byte(`{"nodes":{
	  "model.s.a":{"resource_type":"model","name":"a","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["s","staging","a"]},
	  "model.s.b":{"resource_type":"model","name":"b","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["s","staging","b"]}}}`)
	for _, gran := range []Granularity{GranularityFolder, GranularityLevel} {
		t1, err := Render(one, Options{Granularity: gran})
		if err != nil {
			t.Fatal(err)
		}
		t2, err := Render(two, Options{Granularity: gran})
		if err != nil {
			t.Fatal(err)
		}
		if len(t1) != 1 || len(t2) != 1 {
			t.Fatalf("%s: expected one group either way, got %d and %d", gran, len(t1), len(t2))
		}
		if t1[0].TaskID != t2[0].TaskID {
			t.Errorf("%s: adding a model renamed the task %q -> %q; ids are what depends_on and history are keyed on",
				gran, t1[0].TaskID, t2[0].TaskID)
		}
	}
}

// TestDerivedTaskIDIsRefusedByName covers the last item of #1114. A dbt folder
// name becomes a task id verbatim, and an unusable one — `my folder!` — WAS
// caught, but only later by DAGSpec.Validate, as a JSON-schema dump naming
// dag.json. The compiler knows the folder AND the rule, so it should say which
// folder and why.
func TestDerivedTaskIDIsRefusedByName(t *testing.T) {
	bad := []byte(`{"nodes":{
	  "model.s.x":{"resource_type":"model","name":"x","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["s","my folder!","x"]},
	  "model.s.y":{"resource_type":"model","name":"y","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["s","my folder!","y"]}}}`)
	_, err := Render(bad, Options{Granularity: GranularityFolder})
	if err == nil {
		t.Fatal("expected a refusal naming the folder")
	}
	for _, want := range []string{"my folder!", "granularity"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name the folder and the knob (missing %q)", err, want)
		}
	}
	if strings.Contains(err.Error(), "jsonschema") {
		t.Errorf("the author should not be reading a schema dump: %v", err)
	}
}
