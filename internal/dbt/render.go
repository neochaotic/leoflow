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
	"strings"

	"github.com/neochaotic/leoflow/internal/domain"
)

// Granularity controls how dbt nodes are partitioned into Leoflow tasks.
type Granularity string

// Granularity strategies. node is one task per dbt node; the rest contract a
// group of nodes into one task (a quotient of the dbt DAG), which must stay
// acyclic — semantic strategies (folder/tag) are validated, level is acyclic by
// construction.
const (
	GranularityNode   Granularity = "node"
	GranularityLevel  Granularity = "level"
	GranularityFolder Granularity = "folder"
	GranularityTag    Granularity = "tag"
)

// Options configures a Render call.
type Options struct {
	// Granularity selects the partition strategy; the empty value means node.
	Granularity Granularity
}

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
	ResourceType string   `json:"resource_type"`
	Name         string   `json:"name"`
	FQN          []string `json:"fqn"`
	Tags         []string `json:"tags"`
	DependsOn    struct {
		Nodes []string `json:"nodes"`
	} `json:"depends_on"`
}

// execNode is an executable dbt node with its executable parents resolved.
type execNode struct {
	name    string
	rtype   string
	fqn     []string
	tags    []string
	parents []string // ids of executable parent nodes
}

// Render parses a dbt manifest.json and returns Leoflow bash tasks. At node
// granularity it emits one task per executable node (`dbt <verb> --select`); at a
// grouped granularity it contracts nodes into per-group tasks
// (`dbt build --select`) and rejects a grouping that makes the task graph cyclic.
// Tasks are sorted by TaskID for deterministic output.
func Render(manifestJSON []byte, opts Options) ([]domain.TaskSpec, error) {
	var m manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, fmt.Errorf("parsing dbt manifest: %w", err)
	}

	nodes := make(map[string]execNode, len(m.Nodes))
	for id, n := range m.Nodes {
		if _, ok := dbtVerb[n.ResourceType]; ok {
			nodes[id] = execNode{name: n.Name, rtype: n.ResourceType, fqn: n.FQN, tags: n.Tags}
		}
	}
	for id, n := range m.Nodes {
		en, ok := nodes[id]
		if !ok {
			continue
		}
		for _, parent := range n.DependsOn.Nodes {
			if _, ok := nodes[parent]; ok {
				en.parents = append(en.parents, parent)
			}
		}
		nodes[id] = en
	}

	if opts.Granularity == "" || opts.Granularity == GranularityNode {
		return renderNodes(nodes), nil
	}
	return renderGrouped(nodes, opts.Granularity)
}

// renderNodes emits one task per node, scoped to that node's dbt verb.
func renderNodes(nodes map[string]execNode) []domain.TaskSpec {
	tasks := make([]domain.TaskSpec, 0, len(nodes))
	for _, n := range nodes {
		var deps []string
		if len(n.parents) > 0 {
			deps = make([]string, 0, len(n.parents))
			for _, p := range n.parents {
				deps = append(deps, nodes[p].name)
			}
			sort.Strings(deps)
		}
		tasks = append(tasks, domain.TaskSpec{
			TaskID:     n.name,
			Type:       domain.TaskTypeBash,
			Entrypoint: fmt.Sprintf("dbt %s --select %s", dbtVerb[n.rtype], n.name),
			DependsOn:  deps,
		})
	}
	sortTasks(tasks)
	return tasks
}

// renderGrouped partitions nodes by the strategy, contracts each group into one
// `dbt build --select <members>` task, and fails if the quotient graph is cyclic.
func renderGrouped(nodes map[string]execNode, gran Granularity) ([]domain.TaskSpec, error) {
	levels := topoLevels(nodes)
	groupOf := make(map[string]string, len(nodes))
	for id, n := range nodes {
		groupOf[id] = groupKey(n, gran, levels[id])
	}

	members := map[string][]string{}
	children := map[string]map[string]bool{} // parent group -> child groups
	parents := map[string]map[string]bool{}  // child group -> parent groups
	for id, n := range nodes {
		g := groupOf[id]
		members[g] = append(members[g], n.name)
		for _, p := range n.parents {
			pg := groupOf[p]
			if pg == g {
				continue
			}
			addEdge(children, pg, g)
			addEdge(parents, g, pg)
		}
	}

	if cyc := findCycle(members, children); cyc != nil {
		return nil, fmt.Errorf(
			"dbt grouping by %s introduces a cross-group cycle: %s; use granularity=node or reorganize the project",
			gran, strings.Join(append(cyc, cyc[0]), " -> "))
	}

	tasks := make([]domain.TaskSpec, 0, len(members))
	for g, mem := range members {
		sort.Strings(mem)
		tasks = append(tasks, domain.TaskSpec{
			TaskID:     g,
			Type:       domain.TaskTypeBash,
			Entrypoint: "dbt build --select " + strings.Join(mem, " "),
			DependsOn:  sortedSet(parents[g]),
		})
	}
	sortTasks(tasks)
	return tasks, nil
}

// groupKey returns the group a node belongs to under the given strategy.
func groupKey(n execNode, gran Granularity, level int) string {
	switch gran {
	case GranularityLevel:
		return fmt.Sprintf("level_%d", level)
	case GranularityFolder:
		if len(n.fqn) > 2 {
			return n.fqn[1] // <package>/<folder>/.../<name>
		}
		return n.rtype + "s" // seeds/snapshots carry no folder segment
	case GranularityTag:
		if len(n.tags) > 0 {
			tags := append([]string(nil), n.tags...)
			sort.Strings(tags)
			return "tag_" + tags[0]
		}
		return "untagged"
	default:
		return n.name
	}
}

// topoLevels assigns level(n) = 0 for a node with no executable parents, else
// 1 + max(level(parents)). Grouping by level is acyclic by construction.
func topoLevels(nodes map[string]execNode) map[string]int {
	lvl := make(map[string]int, len(nodes))
	var visit func(id string) int
	visit = func(id string) int {
		if v, ok := lvl[id]; ok {
			return v
		}
		maxLvl := -1
		for _, p := range nodes[id].parents {
			if l := visit(p); l > maxLvl {
				maxLvl = l
			}
		}
		lvl[id] = maxLvl + 1
		return lvl[id]
	}
	for id := range nodes {
		visit(id)
	}
	return lvl
}

// findCycle runs a 3-color DFS over the quotient graph and returns a cycle path
// (group names) or nil. Deterministic via sorted iteration.
func findCycle(members map[string][]string, children map[string]map[string]bool) []string {
	const white, gray, black = 0, 1, 2
	color := make(map[string]int, len(members))
	var stack []string
	var dfs func(g string) []string
	dfs = func(g string) []string {
		color[g] = gray
		stack = append(stack, g)
		for _, to := range sortedSet(children[g]) {
			switch color[to] {
			case gray:
				for i, s := range stack {
					if s == to {
						return append([]string(nil), stack[i:]...)
					}
				}
			case white:
				if c := dfs(to); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[g] = black
		return nil
	}
	for _, g := range sortedSetKeys(members) {
		if color[g] == white {
			if c := dfs(g); c != nil {
				return c
			}
		}
	}
	return nil
}

func addEdge(m map[string]map[string]bool, from, to string) {
	if m[from] == nil {
		m[from] = map[string]bool{}
	}
	m[from][to] = true
}

func sortTasks(tasks []domain.TaskSpec) {
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
}

func sortedSet(s map[string]bool) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSetKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
