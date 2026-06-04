// Package cli implements the leoflow command-line interface.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// NewRootCommand builds the root leoflow command with its global flags and
// subcommands.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "leoflow",
		Short:         "Leoflow is a GitOps-first, container-native workflow orchestrator.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "config file path (default ~/.leoflow/config.yaml)")
	root.PersistentFlags().String("log-level", "", "log level: debug, info, warn, error")
	root.PersistentFlags().String("server-url", "", "control plane API base URL")

	// Cobra command groups (issue #D1, dogfood audit #212): split the 17
	// commands into four navigable sections so new users see "init → run"
	// as one block, not as one long alphabetised list.
	root.AddGroup(
		&cobra.Group{ID: "authoring", Title: "Authoring loop (write → check → package → push):"},
		&cobra.Group{ID: "runtime", Title: "Runtime (start the control plane):"},
		&cobra.Group{ID: "inspection", Title: "Inspection (query DAGs / runs / auth):"},
		&cobra.Group{ID: "lifecycle", Title: "Lifecycle (install, configure, repair, retire):"},
	)

	authoring := []*cobra.Command{newInitCommand(), newValidateCommand(), newCompileCommand(), newPushCommand(), newLoginCommand()}
	runtime := []*cobra.Command{newLiteCommand(), newServerCommand()}
	inspection := []*cobra.Command{newDagsCommand(), newRunsCommand(), newAuthCommand()}
	lifecycle := []*cobra.Command{newSetupCommand(), newDoctorCommand(), newDBCommand(), newUninstallCommand(), newVersionCommand()}

	for _, c := range authoring {
		c.GroupID = "authoring"
		root.AddCommand(c)
	}
	for _, c := range runtime {
		c.GroupID = "runtime"
		root.AddCommand(c)
	}
	for _, c := range inspection {
		c.GroupID = "inspection"
		root.AddCommand(c)
	}
	for _, c := range lifecycle {
		c.GroupID = "lifecycle"
		root.AddCommand(c)
	}

	// gen-docs is a maintenance helper — leave ungrouped so it does not
	// clutter the user-facing sections.
	root.AddCommand(newGenDocsCommand())

	return root
}

// Execute runs the root command and returns a process exit code.
func Execute() int {
	if err := NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
