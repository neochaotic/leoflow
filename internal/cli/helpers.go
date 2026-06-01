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

// loadProjectConfig reads and parses the leoflow.yaml in dir, then applies the
// canonical schema defaults so every downstream consumer sees a fully-populated
// config (DagSource, PythonVersion, exclude paths, Build/Registry defaults).
// Centralizing the defaults here is what lets the multi-DAG workspace synthesize
// a working config from a sparse yaml (or none at all — see DiscoverProjects).
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
	cfg.ApplyDefaults()
	return &cfg, nil
}

// dagSourcePath resolves the DAG source file for a project. The caller is
// expected to have applied schema defaults (cfg.ApplyDefaults), so cfg.DagSource
// is always populated — loadProjectConfig does this automatically.
func dagSourcePath(dir string, cfg *domain.LeoflowConfig) string {
	return filepath.Join(dir, cfg.DagSource)
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
