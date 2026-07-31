package domain

import (
	"errors"
	"strings"
	"testing"
)

// cyclicSpec returns a valid spec whose task graph is the given adjacency.
func specWithDeps(deps map[string][]string) *DAGSpec {
	spec := validDAGSpec()
	spec.Tasks = nil
	for id, on := range deps {
		spec.Tasks = append(spec.Tasks, TaskSpec{
			TaskID: id, Type: TaskTypeBash, Entrypoint: "echo hi",
			DependsOn: on, TriggerRule: TriggerRuleAllSuccess,
		})
	}
	return spec
}

// A cycle has no valid execution order, so no task in it ever becomes ready and
// the run sits in `running` forever with nothing to show an operator. Compile is
// where the author is still looking at the file.
func TestValidateRejectsCycles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		deps  map[string][]string
		names []string
	}{
		{"self dependency", map[string][]string{"a": {"a"}}, []string{"a"}},
		{"two-task cycle", map[string][]string{"a": {"b"}, "b": {"a"}}, []string{"a", "b"}},
		{"three-task cycle", map[string][]string{"a": {"c"}, "b": {"a"}, "c": {"b"}}, []string{"a", "b", "c"}},
		{"cycle beside a healthy chain", map[string][]string{
			"root": nil, "leaf": {"root"}, "x": {"y"}, "y": {"x"},
		}, []string{"x", "y"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := specWithDeps(tc.deps).Validate()
			if err == nil {
				t.Fatal("Validate accepted a cyclic DAG")
			}
			if !errors.Is(err, ErrCyclicDAG) {
				t.Fatalf("error does not wrap ErrCyclicDAG: %v", err)
			}
			// The message must name the tasks in the cycle; "cycle detected" alone
			// leaves the author to find it in a 200-task DAG by hand.
			for _, n := range tc.names {
				if !strings.Contains(err.Error(), n) {
					t.Errorf("error %q does not name task %q", err, n)
				}
			}
		})
	}
}

// A dependency on a task that does not exist is the same class of defect: the
// dependent task can never become ready, so the run hangs. It is also the more
// common typo.
func TestValidateRejectsUnknownDependency(t *testing.T) {
	err := specWithDeps(map[string][]string{"a": nil, "b": {"ghost"}}).Validate()
	if err == nil {
		t.Fatal("Validate accepted a dependency on a nonexistent task")
	}
	if !errors.Is(err, ErrUnknownDependency) {
		t.Fatalf("error does not wrap ErrUnknownDependency: %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "b") {
		t.Errorf("error %q should name both the dangling ref and the task declaring it", err)
	}
}

// A duplicated task id silently drops one definition: the graph keys by id, so
// whichever loses is simply gone, and the DAG runs a subset of what was written.
func TestValidateRejectsDuplicateTaskID(t *testing.T) {
	spec := validDAGSpec()
	spec.Tasks = []TaskSpec{
		{TaskID: "a", Type: TaskTypeBash, Entrypoint: "echo 1", TriggerRule: TriggerRuleAllSuccess},
		{TaskID: "a", Type: TaskTypeBash, Entrypoint: "echo 2", TriggerRule: TriggerRuleAllSuccess},
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("Validate accepted a duplicate task_id")
	}
	if !errors.Is(err, ErrDuplicateTaskID) {
		t.Fatalf("error does not wrap ErrDuplicateTaskID: %v", err)
	}
}

// An acyclic DAG must keep validating, including a diamond (two paths rejoining),
// which a naive visited-set check misreports as a cycle.
func TestValidateAcceptsAcyclicGraphs(t *testing.T) {
	for name, deps := range map[string]map[string][]string{
		"chain":           {"a": nil, "b": {"a"}, "c": {"b"}},
		"diamond":         {"a": nil, "b": {"a"}, "c": {"a"}, "d": {"b", "c"}},
		"fan out":         {"a": nil, "b": {"a"}, "c": {"a"}, "d": {"a"}},
		"fan in":          {"a": nil, "b": nil, "c": {"a", "b"}},
		"disjoint chains": {"a": nil, "b": {"a"}, "x": nil, "y": {"x"}},
		"single task":     {"only": nil},
	} {
		t.Run(name, func(t *testing.T) {
			if err := specWithDeps(deps).Validate(); err != nil {
				t.Errorf("rejected an acyclic DAG (%s): %v", name, err)
			}
		})
	}
}
