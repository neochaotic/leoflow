package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

var (
	// ErrCyclicDAG reports a task graph with no valid execution order.
	ErrCyclicDAG = errors.New("cyclic task graph")
	// ErrUnknownDependency reports a depends_on entry naming no declared task.
	ErrUnknownDependency = errors.New("unknown task dependency")
	// ErrDuplicateTaskID reports two tasks declaring the same task_id.
	ErrDuplicateTaskID = errors.New("duplicate task_id")
)

// validateGraph rejects a task graph that cannot execute.
//
// All three defects share one symptom and it is the worst one available: the run
// starts, no task in the affected region ever becomes ready, and the run sits in
// `running` indefinitely with nothing on screen explaining why. There is no error
// to read, because from the scheduler's side nothing went wrong — it is waiting
// on a predecessor, correctly, forever.
//
// A duplicate task_id is in the same family for a different reason: the graph
// keys by id, so the losing definition is silently dropped and the DAG runs a
// subset of what the author wrote, with no indication that anything was lost.
//
// Checked at compile so the author sees it while still looking at the file, and
// again at registration (DAGSpec.Validate runs in both) so a hand-written or
// machine-generated dag.json cannot bypass the compiler.
func (d *DAGSpec) validateGraph() error {
	byID := make(map[string][]string, len(d.Tasks))
	order := make([]string, 0, len(d.Tasks))
	for _, t := range d.Tasks {
		if _, dup := byID[t.TaskID]; dup {
			return fmt.Errorf("%w: %q is declared more than once", ErrDuplicateTaskID, t.TaskID)
		}
		byID[t.TaskID] = t.DependsOn
		order = append(order, t.TaskID)
	}
	for _, id := range order {
		for _, dep := range byID[id] {
			if _, ok := byID[dep]; !ok {
				return fmt.Errorf("%w: task %q depends on %q, which is not declared in this DAG",
					ErrUnknownDependency, id, dep)
			}
		}
	}
	if cycle := findTaskCycle(order, byID); cycle != nil {
		return fmt.Errorf("%w: %s; no task in this chain can ever become ready, so the run would hang",
			ErrCyclicDAG, strings.Join(cycle, " -> "))
	}
	return nil
}

// findTaskCycle returns one cycle as a path, or nil when the graph is acyclic.
//
// Three-color DFS rather than a visited set: a plain visited set reports a
// diamond (two paths rejoining at one task) as a cycle, and a diamond is one of
// the most ordinary shapes a real DAG has. Only a node still on the current
// stack (gray) closes a cycle; one already finished (black) is a legitimate
// re-encounter.
//
// Iteration follows the declared task order rather than map order, so the same
// DAG always reports the same cycle — an error message that changes between
// identical runs is one nobody trusts.
func findTaskCycle(order []string, deps map[string][]string) []string {
	const (
		white = 0 // unvisited
		gray  = 1 // on the current stack
		black = 2 // fully explored
	)
	color := make(map[string]int, len(order))
	var stack []string

	var visit func(string) []string
	visit = func(id string) []string {
		color[id] = gray
		stack = append(stack, id)
		for _, dep := range deps[id] {
			switch color[dep] {
			case gray:
				// Close the cycle at the point the stack re-enters it, so the path
				// shown is the cycle itself and not the walk that reached it.
				if i := slices.Index(stack, dep); i >= 0 {
					return append(slices.Clone(stack[i:]), dep)
				}
				return []string{dep, dep}
			case white:
				if c := visit(dep); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return nil
	}

	for _, id := range order {
		if color[id] == white {
			if c := visit(id); c != nil {
				return c
			}
		}
	}
	return nil
}
