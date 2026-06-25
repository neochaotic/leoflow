package dbt

import (
	"fmt"
	"sort"

	"github.com/neochaotic/leoflow/internal/domain"
)

// EmbedGroup namespaces a rendered group's tasks under groupName
// (taskID -> "<group>__<taskID>", since task_id forbids dots) and wires its
// external edges: the upstream task_ids become dependencies of the group's roots
// (tasks with no internal dependency). It returns the namespaced tasks and the
// group's leaf ids (tasks that nothing inside the group depends on) so the caller
// can attach them to the group's downstream tasks (ADR 0043).
func EmbedGroup(groupName string, tasks []domain.TaskSpec, upstream []string) ([]domain.TaskSpec, []string, error) {
	if groupName == "" {
		return nil, nil, fmt.Errorf("dbt group name is required")
	}
	ns := func(id string) string { return groupName + "__" + id }

	hasDependent := make(map[string]bool, len(tasks))
	embedded := make([]domain.TaskSpec, 0, len(tasks))
	for _, t := range tasks {
		nt := t
		nt.TaskID = ns(t.TaskID)
		if len(t.DependsOn) == 0 {
			// a root: inherit the group's external upstream
			nt.DependsOn = append([]string(nil), upstream...)
		} else {
			deps := make([]string, 0, len(t.DependsOn))
			for _, d := range t.DependsOn {
				deps = append(deps, ns(d))
				hasDependent[ns(d)] = true
			}
			nt.DependsOn = deps
		}
		embedded = append(embedded, nt)
	}
	sort.Slice(embedded, func(i, j int) bool { return embedded[i].TaskID < embedded[j].TaskID })

	var leaves []string
	for _, t := range embedded {
		if !hasDependent[t.TaskID] {
			leaves = append(leaves, t.TaskID)
		}
	}
	sort.Strings(leaves)
	return embedded, leaves, nil
}
