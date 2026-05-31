package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate leoflow.yaml and the DAG source against the schema.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			cfg, err := loadProjectConfig(dir)
			if err != nil {
				return err
			}
			if verr := cfg.Validate(); verr != nil {
				return fmt.Errorf("invalid %s: %w", projectConfigPath(dir), verr)
			}
			dagSrc := dagSourcePath(dir, cfg)
			if _, serr := os.Stat(dagSrc); serr != nil {
				return fmt.Errorf("DAG source not found: %w", serr)
			}
			if perr := checkDagPythonSyntax(cmd, dagSrc); perr != nil {
				return perr
			}
			if _, werr := fmt.Fprintf(cmd.OutOrStdout(), "%s is valid\n", projectConfigPath(dir)); werr != nil {
				return werr
			}
			return nil
		},
	}
}

// checkDagPythonSyntax runs `python -m py_compile` on the DAG source to catch
// syntax errors before push (issue #D8 — validate used to lie about a broken
// dag.py). The check is best-effort: when no Python interpreter is reachable
// (managed or system), we warn instead of failing — a fresh install that has
// not yet run `leoflow setup` should still be able to lint its leoflow.yaml.
func checkDagPythonSyntax(cmd *cobra.Command, dagPath string) error {
	py := findPythonForValidate()
	if py == "" {
		if _, werr := fmt.Fprintln(cmd.ErrOrStderr(),
			"warning: skipping dag.py syntax check (no python3 found; run `leoflow setup` to provision one)"); werr != nil {
			return werr
		}
		return nil
	}
	out, err := exec.CommandContext(cmd.Context(), py, "-m", "py_compile", dagPath).CombinedOutput() //nolint:gosec // py + dagPath are both validated inputs from this CLI's own setup/user-arg path
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("dag.py has a syntax error: %s", msg)
}

// findPythonForValidate prefers the managed interpreter that `leoflow setup`
// installs (~/.leoflow/python/bin/python3.11), then falls back to whatever
// `python3` resolves to on PATH. Empty when neither is available.
func findPythonForValidate() string {
	if home, err := os.UserHomeDir(); err == nil {
		managed := home + "/.leoflow/python/bin/python3.11"
		if _, err := os.Stat(managed); err == nil {
			return managed
		}
	}
	if p, err := exec.LookPath("python3"); err == nil {
		return p
	}
	return ""
}
