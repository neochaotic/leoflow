package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const leoflowTemplate = `schema_version: "1.0"
dag_id: %s
description: An example Leoflow DAG.
python_version: "3.11"
dependencies: []
`

const dagTemplate = `"""%s — example Leoflow DAG."""
from __future__ import annotations

from airflow.sdk import dag, task


@dag(schedule="@daily", catchup=False, tags=["example"])
def %s():
    @task
    def hello() -> str:
        return "hello from leoflow"

    hello()


%s()
`

func newInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init <path>",
		Short: "Scaffold a new DAG project (leoflow.yaml + dag.py).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			dagID := filepath.Base(filepath.Clean(dir))
			funcName := strings.NewReplacer("-", "_", ".", "_").Replace(dagID)

			if err := os.MkdirAll(dir, 0o750); err != nil {
				return fmt.Errorf("creating project directory: %w", err)
			}
			files := map[string]string{
				"leoflow.yaml": fmt.Sprintf(leoflowTemplate, dagID),
				"dag.py":       fmt.Sprintf(dagTemplate, dagID, funcName, funcName),
			}
			for name, content := range files {
				p := filepath.Join(dir, name)
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil { //nolint:gosec // G306: scaffold files are non-sensitive source.
					return fmt.Errorf("writing %s: %w", name, err)
				}
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Initialized Leoflow project %q in %s\n", dagID, dir)
			return err
		},
	}
}
