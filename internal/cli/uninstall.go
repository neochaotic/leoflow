package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/config"
)

// newUninstallCommand removes the Leoflow installation (~/.leoflow) and this
// user's Docker datastore volumes, so a reinstall starts fresh. It preserves the
// DAG workspace unless --purge. Confirms first unless --yes.
func newUninstallCommand() *cobra.Command {
	var yes, purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the Leoflow installation (~/.leoflow) and datastore.",
		Long: "uninstall removes the managed Leoflow home (~/.leoflow): the binaries, config, " +
			"managed Python, Monaco assets, and local dev state. It also drops this user's Docker " +
			"datastore volumes (Postgres/Redis) so a reinstall starts clean. It KEEPS your DAG " +
			"workspace unless you pass --purge. It asks for confirmation unless --yes is given. " +
			"(To upgrade instead, just re-run install.sh — it replaces the binaries and keeps your " +
			"config and datastore.)",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUninstall(cmd, yes, purge)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove the DAG workspace (the datastore volumes are removed by default)")
	return cmd
}

func runUninstall(cmd *cobra.Command, yes, purge bool) error {
	out := cmd.OutOrStdout()
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home dir: %w", err)
	}
	root := filepath.Join(home, ".leoflow")
	if _, serr := os.Stat(root); errors.Is(serr, os.ErrNotExist) {
		devPrintf(out, "Nothing to remove: %s does not exist.\n", root)
		return nil
	}

	// Read the workspace path before deleting, so --purge can remove it too.
	workspace := ""
	if c, lerr := config.Load(filepath.Join(root, "config.yaml"), nil); lerr == nil {
		workspace = c.Workspace
	}

	binDir := installBinDir()
	devPrintf(out, "This will remove the Leoflow installation:\n  %s  (config, managed Python, Monaco, sources)\n", root)
	if binDir != "" {
		devPrintf(out, "  the leoflow binaries in %s\n", binDir)
	}
	devPrintln(out, "  the Docker datastore volumes (Postgres/Redis) — a reinstall then starts fresh")
	if purge && workspace != "" {
		devPrintf(out, "  %s  (your DAG workspace — only with --purge)\n", workspace)
	}

	if !yes && !confirmUninstall(cmd) {
		devPrintln(out, "aborted.")
		return nil
	}

	// Always drop this user's datastore volumes so a reinstall starts clean — the
	// stale-admin/old-runs trap. The DAG workspace is preserved (only --purge
	// removes it). Targets the per-user compose project by name, so it works
	// regardless of which compose file Lite used.
	composeDownVolumes(cmd)
	if rerr := os.RemoveAll(root); rerr != nil {
		return fmt.Errorf("removing %s: %w", root, rerr)
	}
	devPrintf(out, "✓ removed %s\n", root)
	// Remove the binaries too — install.sh places them on a PATH dir (e.g.
	// /usr/local/bin), NOT under ~/.leoflow, so removing the home alone left a
	// working `leoflow` behind.
	removeBinariesIn(out, installBinDir())
	if purge && workspace != "" {
		if rerr := os.RemoveAll(workspace); rerr != nil {
			devPrintf(out, "  ! could not remove workspace %s: %v\n", workspace, rerr)
		} else {
			devPrintf(out, "✓ removed workspace %s\n", workspace)
		}
	}
	devPrintln(out, "Done. If install.sh added a 'leoflow' PATH line to your shell profile, remove it.")
	return nil
}

// installBinDir is the directory the running leoflow binary lives in — where
// install.sh placed the binaries (/usr/local/bin, ~/.local/bin, or ~/.leoflow/bin).
func installBinDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// removeBinariesIn deletes the leoflow binaries from dir (leoflow last — it is the
// running process; on Linux unlinking a running binary is safe). A removal failure
// (e.g. /usr/local/bin without sudo) is reported, not fatal, so ~/.leoflow is still
// cleaned.
func removeBinariesIn(out io.Writer, dir string) {
	if dir == "" {
		return
	}
	for _, name := range []string{"leoflow-server", "leoflow-agent", "leoflow"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := os.Remove(p); err != nil {
			devPrintf(out, "  ! could not remove %s: %v (remove it manually, e.g. `sudo rm %s`)\n", p, err, p)
		} else {
			devPrintf(out, "✓ removed %s\n", p)
		}
	}
}

// confirmUninstall reads a yes/no from stdin; anything but yes/y (and any EOF on
// a non-interactive stdin) aborts, so the destructive action is never taken by
// accident.
func confirmUninstall(cmd *cobra.Command) bool {
	devPrintf(cmd.OutOrStdout(), "Type 'yes' to confirm (or re-run with --yes): ")
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "yes" || answer == "y"
}

// composeDownVolumes best-effort tears down this user's Lite datastore — the
// per-user compose project's containers and named volumes — so a reinstall starts
// fresh. It targets the project by NAME (-p leoflow-<uid>), independent of which
// compose file `leoflow lite` used (a checkout's docker-compose.dev.yaml, the
// managed ~/.leoflow one, or --compose), and then removes the project's volumes
// directly (a file-less `down` does not know the volume definitions to drop). All
// best-effort: an already-gone datastore is fine.
func composeDownVolumes(cmd *cobra.Command) {
	if _, err := exec.LookPath("docker"); err != nil {
		return
	}
	ctx := cmdContext(cmd)
	project := liteComposeProject()
	down := exec.CommandContext(ctx, "docker", "compose", "-p", project, "down", "--remove-orphans") //nolint:gosec // fixed args + derived project name
	down.Stdout, down.Stderr = io.Discard, io.Discard
	_ = down.Run() //nolint:errcheck // best-effort: containers may already be gone; volume rm below is the real cleanup
	// Remove the project's named volumes (prefix "<project>_"; the trailing "_"
	// avoids matching a different uid like leoflow-5011_*).
	out, err := exec.CommandContext(ctx, "docker", "volume", "ls", "-q", "--filter", "name="+project+"_").Output() //nolint:gosec // derived project name
	if err != nil {
		return
	}
	removed := 0
	for _, v := range strings.Fields(string(out)) {
		if exec.CommandContext(ctx, "docker", "volume", "rm", v).Run() == nil { //nolint:gosec // volume name from docker's own listing
			removed++
		}
	}
	if removed > 0 {
		devPrintf(cmd.OutOrStdout(), "  ✓ removed %d Lite datastore volume(s)\n", removed)
	}
}
