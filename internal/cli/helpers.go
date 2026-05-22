package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	yaml "go.yaml.in/yaml/v3"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/domain"
)

// projectConfigPath returns the path to leoflow.yaml inside a project directory.
func projectConfigPath(dir string) string {
	return filepath.Join(dir, "leoflow.yaml")
}

// loadProjectConfig reads and parses the leoflow.yaml in dir.
func loadProjectConfig(dir string) (*domain.LeoflowConfig, error) {
	p := projectConfigPath(dir)
	data, err := os.ReadFile(p) //nolint:gosec // G304: project path is supplied by the operator on the CLI.
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	var cfg domain.LeoflowConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	return &cfg, nil
}

// dagSourcePath resolves the DAG source file for a project, defaulting to dag.py.
func dagSourcePath(dir string, cfg *domain.LeoflowConfig) string {
	src := cfg.DagSource
	if src == "" {
		src = "dag.py"
	}
	return filepath.Join(dir, src)
}

// configFilePath returns the config file to load: the --config flag when set,
// otherwise the default path when it exists, otherwise empty (defaults + env).
func configFilePath(cmd *cobra.Command) string {
	if p, err := cmd.Flags().GetString("config"); err == nil && p != "" {
		return p
	}
	def, err := config.DefaultConfigFile()
	if err != nil {
		return ""
	}
	if _, err := os.Stat(def); err == nil {
		return def
	}
	return ""
}
