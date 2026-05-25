package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	leoflow "github.com/neochaotic/leoflow"
	"github.com/neochaotic/leoflow/internal/setup"
)

// parserTaskSDK pins the Airflow Task SDK installed alongside the parser, so the
// parser venv matches the task image and runtime (see dev.go taskSDKVersion).
const parserTaskSDK = "apache-airflow-task-sdk==1.2.1"

// setupManifest records what `leoflow setup` provisioned, so later runs and
// `leoflow doctor` can report the managed state.
type setupManifest struct {
	Python     string    `json:"python"`
	Workspace  string    `json:"workspace"`
	Tier       string    `json:"tier"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	ParserVenv string    `json:"parser_venv,omitempty"`
	ParserCmd  string    `json:"parser_cmd,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// newSetupCommand bootstraps the managed runtime: a usable Python 3.11 (the
// system one or a downloaded relocatable CPython), the ~/.leoflow layout, and a
// workspace directory for the user's DAG projects.
func newSetupCommand() *cobra.Command {
	var workspace string
	var dryRun, skipPythonDeps bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Bootstrap the managed Leoflow runtime (Python, parser venv, workspace).",
		Long: "setup prepares ~/.leoflow: it ensures a Python 3.11 is available " +
			"(using a system interpreter if present, otherwise downloading a pinned, " +
			"checksum-verified relocatable CPython — no sudo, no system packages), " +
			"extracts the embedded parser and runtime sources, provisions a parser venv " +
			"(Airflow — done once, then cached), and creates a workspace directory. " +
			"Re-running is safe. Use --dry-run to see the plan, or --skip-python-deps to " +
			"install binaries and Python only (e.g. when parsing happens in containers).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd, workspace, dryRun, skipPythonDeps)
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace dir for your DAG projects (default ~/leoflow)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "detect and print the plan without downloading or writing anything")
	cmd.Flags().BoolVar(&skipPythonDeps, "skip-python-deps", false, "skip the parser venv (Airflow) install")
	return cmd
}

func runSetup(cmd *cobra.Command, workspace string, dryRun, skipPythonDeps bool) error {
	out := cmd.OutOrStdout()
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	leoflowHome := filepath.Join(homeDir, ".leoflow")
	if workspace == "" {
		workspace = filepath.Join(homeDir, "leoflow")
	}

	r := setup.Detect(setup.Probe{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		LookPath: exec.LookPath, Stat: os.Stat, Getwd: os.Getwd,
	})

	_, _ = fmt.Fprintf(out, "leoflow setup\n\n  platform   %s/%s%s\n  tier       %s\n  workspace  %s\n\n", //nolint:errcheck // best-effort terminal output
		r.OS, r.Arch, libcSuffix(r.Libc), r.Tier, workspace)

	if r.Python311 {
		_, _ = fmt.Fprintf(out, "  python     using system %s\n", r.PythonPath) //nolint:errcheck // best-effort terminal output
	} else {
		_, _ = fmt.Fprintln(out, "  python     none on PATH; will install a relocatable CPython 3.11 under ~/.leoflow/python") //nolint:errcheck // best-effort terminal output
	}

	if dryRun {
		_, _ = fmt.Fprintln(out, "\n  (dry run: nothing downloaded or written)") //nolint:errcheck // best-effort terminal output
		return nil
	}

	if mkErr := os.MkdirAll(leoflowHome, 0o750); mkErr != nil {
		return fmt.Errorf("creating %s: %w", leoflowHome, mkErr)
	}
	pyPath, pyErr := setup.EnsurePython(cmd.Context(), setup.EnsureOpts{
		Home: leoflowHome, GOOS: r.OS, GOARCH: r.Arch, Libc: r.Libc,
		LookPath: exec.LookPath, Stat: os.Stat,
		Logf: func(format string, a ...any) {
			_, _ = fmt.Fprintf(out, "  "+format+"\n", a...) //nolint:errcheck // best-effort terminal output
		},
	})
	if pyErr != nil {
		return fmt.Errorf("ensuring Python: %w", pyErr)
	}

	pysrcDir := filepath.Join(leoflowHome, "pysrc")
	if exErr := setup.ExtractFS(leoflow.PythonSources(), pysrcDir); exErr != nil {
		return fmt.Errorf("extracting embedded Python sources: %w", exErr)
	}
	_, _ = fmt.Fprintf(out, "  sources    extracted parser + runtime to %s\n", pysrcDir) //nolint:errcheck // best-effort terminal output

	var parserVenv, parserCmd string
	if skipPythonDeps {
		_, _ = fmt.Fprintln(out, "  parser     skipped (--skip-python-deps)") //nolint:errcheck // best-effort terminal output
	} else {
		venvPy, pErr := provisionParserVenv(cmd, out, leoflowHome, pysrcDir, pyPath)
		if pErr != nil {
			return pErr
		}
		parserVenv = filepath.Join(leoflowHome, "parser-venv")
		parserCmd = venvPy + " -m leoflow_parser"
		wrote, cErr := writeParserConfig(leoflowHome, parserCmd)
		if cErr != nil {
			return fmt.Errorf("writing parser config: %w", cErr)
		}
		if wrote {
			_, _ = fmt.Fprintf(out, "  parser     parser_cmd set to %q\n", parserCmd) //nolint:errcheck // best-effort terminal output
		} else {
			_, _ = fmt.Fprintf(out, "  parser     ~/.leoflow/config.yaml exists; ensure parser_cmd: %q\n", parserCmd) //nolint:errcheck // best-effort terminal output
		}
	}

	if wsErr := os.MkdirAll(workspace, 0o750); wsErr != nil {
		return fmt.Errorf("creating workspace %s: %w", workspace, wsErr)
	}
	if wErr := writeSetupManifest(leoflowHome, setupManifest{
		Python: pyPath, Workspace: workspace, Tier: r.Tier.String(),
		OS: r.OS, Arch: r.Arch, ParserVenv: parserVenv, ParserCmd: parserCmd,
		UpdatedAt: time.Now().UTC(),
	}); wErr != nil {
		return fmt.Errorf("writing setup manifest: %w", wErr)
	}

	if r.UnderMnt {
		_, _ = fmt.Fprintln(out, "\n  WARNING: under /mnt (WSL): keep DAG projects in the WSL native FS (~/...) for hot-reload.") //nolint:errcheck // best-effort terminal output
	}
	_, _ = fmt.Fprintf(out, "\n  ready. Next: `leoflow dev %s/<your-dag>` (creates the task venv on first run).\n", workspace) //nolint:errcheck // best-effort terminal output
	return nil
}

// provisionParserVenv creates (or reuses) the parser venv under ~/.leoflow and
// installs the extracted parser plus the Airflow Task SDK into it. The heavy
// Airflow install runs once; an existing venv interpreter is reused.
func provisionParserVenv(cmd *cobra.Command, out io.Writer, leoflowHome, pysrcDir, pyPath string) (string, error) {
	venvDir := filepath.Join(leoflowHome, "parser-venv")
	venvPy := filepath.Join(venvDir, "bin", "python")
	if _, err := os.Stat(venvPy); err == nil {
		_, _ = fmt.Fprintf(out, "  parser     reusing existing venv at %s\n", venvDir) //nolint:errcheck // best-effort terminal output
		return venvPy, nil
	}
	_, _ = fmt.Fprintln(out, "  parser     provisioning venv (installing Airflow — this runs once)...") //nolint:errcheck // best-effort terminal output
	run := func(ctx context.Context, name string, args ...string) error {
		c := exec.CommandContext(ctx, name, args...) //nolint:gosec // name/args are the managed interpreter and fixed install targets
		c.Stdout, c.Stderr = out, cmd.ErrOrStderr()
		return c.Run()
	}
	venvPy, err := setup.ProvisionVenv(cmd.Context(), run, pyPath, venvDir,
		[]string{filepath.Join(pysrcDir, "parser"), parserTaskSDK})
	if err != nil {
		return "", fmt.Errorf("provisioning parser venv: %w", err)
	}
	return venvPy, nil
}

// writeParserConfig writes parser_cmd to ~/.leoflow/config.yaml when that file
// does not yet exist, returning whether it wrote. An existing config is left
// untouched so a user's customizations are never clobbered.
func writeParserConfig(leoflowHome, parserCmd string) (bool, error) {
	path := filepath.Join(leoflowHome, "config.yaml")
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	content := fmt.Sprintf("# Written by `leoflow setup`.\nparser_cmd: %q\n", parserCmd)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// writeSetupManifest persists the provisioning manifest to ~/.leoflow/setup.json.
func writeSetupManifest(leoflowHome string, m setupManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(leoflowHome, "setup.json"), data, 0o600)
}

// libcSuffix renders " (musl)" / " (glibc)" or nothing for platforms without a
// reported libc (darwin).
func libcSuffix(libc string) string {
	if libc == "" {
		return ""
	}
	return " (" + libc + ")"
}
