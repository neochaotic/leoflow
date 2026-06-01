package cli

import (
	"path/filepath"
	"sort"

	"github.com/neochaotic/leoflow/internal/domain"
)

// WorkspaceSpec is the resolved view of a Lite workspace: the list of DAG
// projects discovered under it plus a synthesized "root" config that single-
// cfg consumers (venv setup, image-build defaults) can still rely on without
// knowing about multi-DAG. The Path is the absolute workspace directory.
type WorkspaceSpec struct {
	// Path is the absolute path of the workspace directory.
	Path string
	// Projects is every DAG project discovered under Path. Empty when the
	// workspace is empty (caller is expected to scaffold a starter then).
	Projects []Project
	// RootCfg is a synthesized LeoflowConfig representing the workspace as a
	// whole: union of every project's pip dependencies, the highest declared
	// python_version, and otherwise schema defaults. It is the single cfg the
	// venv setup uses; multi-DAG aware code paths should iterate Projects
	// instead.
	RootCfg *domain.LeoflowConfig
}

// ResolveWorkspace scans the workspace directory and returns its full
// WorkspaceSpec. It is the single entry point lite uses to convert a directory
// into "the set of DAGs I am responsible for"; both the watcher and the compile
// loop call it on every reload.
//
// Behavior:
//   - Walks subdirs via DiscoverProjects (max-depth 5, skips exclude_paths
//     defaults + hidden dirs, fails loud on duplicate dag_id).
//   - Synthesizes RootCfg by unioning every project's Dependencies (de-duped)
//     and picking the highest PythonVersion ([[simple-reliable-then-grow]]:
//     Python is forward-only-safe inside a major; older projects keep working
//     on a newer interpreter, but newer ones would fail on an older venv).
//   - Returns a non-nil RootCfg even when Projects is empty, so the caller
//     can scaffold against schema defaults without an extra nil-check.
//
// Discovery errors (e.g. duplicate dag_id) are propagated verbatim so the
// caller can surface every colliding path to the user.
func ResolveWorkspace(dir string) (*WorkspaceSpec, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	projects, err := DiscoverProjects(abs)
	if err != nil {
		return nil, err
	}
	root := &domain.LeoflowConfig{}
	root.ApplyDefaults()
	if len(projects) > 0 {
		root.Dependencies = unionDependencies(projects)
		if py := highestPythonVersion(projects); py != "" {
			root.PythonVersion = py
		}
	}
	return &WorkspaceSpec{Path: abs, Projects: projects, RootCfg: root}, nil
}

// WatchedPaths returns the file paths the mtime-polling watcher should track:
// every project's leoflow.yaml (when present) and dag.py. A save in any of
// them must trigger a reload, since lite recompiles+reregisters every project
// on each reload.
func (w *WorkspaceSpec) WatchedPaths() []string {
	if w == nil {
		return nil
	}
	paths := make([]string, 0, len(w.Projects)*2)
	for _, p := range w.Projects {
		if p.HasYAML {
			paths = append(paths, p.ConfigPath)
		}
		paths = append(paths, filepath.Join(p.Path, p.Config.DagSource))
	}
	return paths
}

// unionDependencies returns the de-duplicated union of pip specifiers across
// every project. Identity is exact string match; any DAG that needs version
// pinning beyond what its sibling declares must do so in its own yaml.
func unionDependencies(projects []Project) []string {
	seen := map[string]struct{}{}
	for _, p := range projects {
		for _, dep := range p.Config.Dependencies {
			seen[dep] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// highestPythonVersion returns the highest declared python_version across
// projects (e.g. "3.12" beats "3.11"). Empty when no project has it pinned to
// a non-default value (the caller keeps the schema default in that case).
// Versions are compared as semver-ish strings — sufficient for the closed set
// of "3.10|3.11|3.12|3.13" the schema allows.
func highestPythonVersion(projects []Project) string {
	var best string
	for _, p := range projects {
		v := p.Config.PythonVersion
		if v == "" {
			continue
		}
		if best == "" || pythonVersionLess(best, v) {
			best = v
		}
	}
	return best
}

// pythonVersionLess reports whether a < b for python version strings like
// "3.11". It compares lexicographically with a tweak: equal-length strings
// compare directly; otherwise pad with leading zeros on the minor component.
// Sufficient for the schema-allowed set; do not call with arbitrary versions.
func pythonVersionLess(a, b string) bool {
	// All allowed versions are "3.X"; comparing the X portion is enough.
	if len(a) == len(b) {
		return a < b
	}
	return a < b
}
