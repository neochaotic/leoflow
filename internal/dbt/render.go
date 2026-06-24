// Package dbt renders a dbt project's manifest.json into Leoflow tasks (ADR 0042).
//
// dbt already compiles a project into target/manifest.json — the canonical DAG of
// nodes (seeds, models, snapshots, tests) with their dependencies. This package
// reads that file in Go and emits flat Leoflow tasks, so a dbt project becomes a
// Leoflow DAG with no Cosmos at runtime and no Airflow in the parser.
package dbt

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/neochaotic/leoflow/internal/domain"
)

// dbtVerb maps each executable dbt resource type to the dbt subcommand that runs
// a single node of that type via --select. Non-executable resources (sources,
// macros, exposures, …) are absent and never become tasks.
var dbtVerb = map[string]string{
	"seed":     "seed",
	"model":    "run",
	"snapshot": "snapshot",
	"test":     "test",
}

// manifest is the subset of dbt's manifest.json this package reads.
type manifest struct {
	Nodes map[string]manifestNode `json:"nodes"`
}

type manifestNode struct {
	ResourceType string `json:"resource_type"`
	Name         string `json:"name"`
	DependsOn    struct {
		Nodes []string `json:"nodes"`
	} `json:"depends_on"`
}

// Render parses a dbt manifest.json and returns one flat bash task per executable
// node (node granularity), wiring each task's DependsOn from the node's
// depends_on.nodes (filtered to nodes that became tasks). Tasks are sorted by
// TaskID for deterministic output.
func Render(manifestJSON []byte) ([]domain.TaskSpec, error) {
	var m manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, fmt.Errorf("parsing dbt manifest: %w", err)
	}

	// Map each executable node's unique_id to its task_id so dependency edges can
	// be filtered (drop non-executable parents) and translated to task_ids.
	taskID := make(map[string]string, len(m.Nodes))
	for id, n := range m.Nodes {
		if _, ok := dbtVerb[n.ResourceType]; ok {
			taskID[id] = n.Name
		}
	}

	tasks := make([]domain.TaskSpec, 0, len(taskID))
	for _, n := range m.Nodes {
		verb, ok := dbtVerb[n.ResourceType]
		if !ok {
			continue
		}
		var deps []string
		for _, parent := range n.DependsOn.Nodes {
			if tid, ok := taskID[parent]; ok {
				deps = append(deps, tid)
			}
		}
		sort.Strings(deps)
		tasks = append(tasks, domain.TaskSpec{
			TaskID:     n.Name,
			Type:       domain.TaskTypeBash,
			Entrypoint: fmt.Sprintf("dbt %s --select %s", verb, n.Name),
			DependsOn:  deps,
		})
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	return tasks, nil
}
