package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// announceNotImplemented prints a stable message for commands whose behavior
// arrives in a later phase, returning any write error.
func announceNotImplemented(cmd *cobra.Command, feature string) error {
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s is not yet implemented (arriving in a later phase).\n", feature)
	return err
}

func newServerCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "server",
		Short: "Run the Leoflow control plane (Phase 2).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return announceNotImplemented(cmd, "server")
		},
	}
}
