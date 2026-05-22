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

func newAuthCommand() *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication tokens and users (Phase 2).",
	}
	auth.AddCommand(&cobra.Command{
		Use:   "create-token",
		Short: "Issue a JWT for the configured user (Phase 2).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return announceNotImplemented(cmd, "auth create-token")
		},
	})
	return auth
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
