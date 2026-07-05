package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/domain"
)

// loadDbtManifest returns the dbt manifest.json bytes. A pinned dbt.manifest is used
// as-is; otherwise it runs a fresh `dbt parse` so a model edit reflects on hot-reload
// — and in Lite (local) it parses with the per-DAG venv's dbt rather than a system one
// the user may not have (L1). The manifest is resolved relative to the dbt project.
//
// The fresh-parse path shells out to the dbt binary — an external-process
// orchestration only honestly exercised end-to-end (the e2e-dbt job), so this file is
// excluded from the unit-coverage floor (ADR 0011), like the other external-binary
// orchestrators (managed_postgres.go, …). The pure branches (a pinned manifest, the
// dbt-not-found error, the venv/profile resolution) are unit-tested via their helpers.
func loadDbtManifest(cmd *cobra.Command, dir string, c *domain.DbtConfig, local bool, dagID string) ([]byte, error) {
	projectDir := filepath.Join(dir, c.Project)
	if c.Manifest != "" {
		path := filepath.Join(projectDir, c.Manifest)
		data, rerr := os.ReadFile(path) //nolint:gosec // G304: operator-supplied project path.
		if rerr != nil {
			return nil, fmt.Errorf("reading dbt manifest %s: %w", path, rerr)
		}
		return data, nil
	}
	dbtBin := "dbt"
	if local {
		if v := liteDbtBin(dagID); v != "" {
			dbtBin = v
		}
	}
	if dbtBin == "dbt" {
		if _, lerr := exec.LookPath("dbt"); lerr != nil {
			return nil, fmt.Errorf("dbt is not on PATH: install dbt-core and your adapter (e.g. `pip install dbt-postgres`), or set dbt.manifest in leoflow.yaml to a pre-built manifest.json")
		}
	}
	pc := exec.CommandContext(cmdContext(cmd), dbtBin, "parse") //nolint:gosec // dbtBin is the resolved venv or PATH dbt
	pc.Dir = projectDir
	pc.Env = os.Environ()
	// Zero-config local warehouse: `dbt parse` needs a profile too, so give it a
	// default duckdb one when the Lite project has no connection and no profiles.yml
	// — the compile-time half of L4 (the runtime writes the same at task time).
	if local && c.Connection == "" {
		if pdir := writeParseDuckdbProfile(dir, c); pdir != "" {
			defer func() { _ = os.RemoveAll(pdir) }() //nolint:errcheck // best-effort cleanup of a temp dir
			pc.Env = append(pc.Env, "DBT_PROFILES_DIR="+pdir)
		}
	}
	pc.Stderr = cmd.ErrOrStderr()
	if rerr := pc.Run(); rerr != nil {
		return nil, fmt.Errorf("dbt parse in %s: %w", projectDir, rerr)
	}
	path := filepath.Join(projectDir, "target", "manifest.json")
	data, rerr := os.ReadFile(path) //nolint:gosec // derived from operator-supplied project path.
	if rerr != nil {
		return nil, fmt.Errorf("reading dbt manifest %s: %w", path, rerr)
	}
	return data, nil
}
