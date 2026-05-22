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

	root.AddCommand(
		newVersionCommand(),
		newInitCommand(),
		newValidateCommand(),
		newCompileCommand(),
		newPushCommand(),
		newAuthCommand(),
		newServerCommand(),
	)
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
