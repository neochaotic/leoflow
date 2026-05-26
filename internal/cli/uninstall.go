package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/config"
)

// newUninstallCommand removes the Leoflow installation (~/.leoflow). It confirms
// first (unless --yes); --purge additionally removes the DAG workspace and the
// Docker datastore volumes.
func newUninstallCommand() *cobra.Command {
	var yes, purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the Leoflow installation (~/.leoflow).",
		Long: "uninstall removes the managed Leoflow home (~/.leoflow): the binaries, config, " +
			"managed Python, Monaco assets, and local dev state. It does NOT remove your DAG " +
			"workspace or Docker datastore volumes unless you pass --purge. It asks for confirmation " +
			"unless --yes is given. (To upgrade instead, just re-run install.sh — it replaces the " +
			"binaries and keeps your config.)",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUninstall(cmd, yes, purge)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove the DAG workspace and Docker datastore volumes (destructive)")
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

	devPrintf(out, "This will remove the Leoflow installation:\n  %s  (binaries, config, Python, Monaco)\n", root)
	if purge {
		if workspace != "" {
			devPrintf(out, "  %s  (your DAG workspace)\n", workspace)
		}
		devPrintln(out, "  Docker datastore volumes: leoflow_pgdata, leoflow_redisdata")
	}

	if !yes && !confirmUninstall(cmd) {
		devPrintln(out, "aborted.")
		return nil
	}

	if purge {
		// Best-effort: stop datastores and drop their volumes before deleting the
		// compose file that defines them.
		composeDownVolumes(cmd, filepath.Join(root, "docker-compose.yaml"))
	}
	if rerr := os.RemoveAll(root); rerr != nil {
		return fmt.Errorf("removing %s: %w", root, rerr)
	}
	devPrintf(out, "✓ removed %s\n", root)
	if purge && workspace != "" {
		if rerr := os.RemoveAll(workspace); rerr != nil {
			devPrintf(out, "  ! could not remove workspace %s: %v\n", workspace, rerr)
		} else {
			devPrintf(out, "✓ removed workspace %s\n", workspace)
		}
	}
	devPrintln(out, "Done. Remove the 'export PATH=\"$HOME/.leoflow/bin:$PATH\"' line from your shell profile if you added it.")
	return nil
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

// composeDownVolumes best-effort stops the datastores and removes their volumes
// via the managed compose file, if both Docker and the file are present.
func composeDownVolumes(cmd *cobra.Command, composeFile string) {
	if _, err := os.Stat(composeFile); err != nil {
		return
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return
	}
	c := exec.CommandContext(cmdContext(cmd), "docker", "compose", "-f", composeFile, "down", "-v") //nolint:gosec // fixed args, managed compose path
	c.Stdout, c.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		// Best-effort: a missing/already-down stack is fine; the files are removed next.
		devPrintf(cmd.OutOrStdout(), "  ! docker compose down (datastores may already be gone): %v\n", err)
	}
}
