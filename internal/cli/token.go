package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/config"
	apiclient "github.com/neochaotic/leoflow/pkg/client"
)

func newAuthCommand() *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication tokens.",
	}
	auth.AddCommand(newCreateTokenCommand())
	auth.AddCommand(newLoginCommand())
	return auth
}

func newCreateTokenCommand() *cobra.Command {
	var serverURL, username, password string
	cmd := &cobra.Command{
		Use:   "create-token",
		Short: "Obtain a JWT from the control plane.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if serverURL == "" {
				cfg, cerr := config.Load(configFilePath(cmd), cmd.Flags())
				if cerr != nil {
					return cerr
				}
				serverURL = cfg.ServerURL
			}
			token, err := requestToken(cmdContext(cmd), serverURL, username, password)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), token)
			return err
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&username, "username", os.Getenv("LEOFLOW_USERNAME"), "username")
	cmd.Flags().StringVar(&password, "password", os.Getenv("LEOFLOW_PASSWORD"), "password")
	return cmd
}

// requestToken posts credentials to /auth/token and returns the access token,
// via the shared typed /api/v2 client (ADR 0050 D8). No token is presented to
// obtain a token, so New is called with an empty bearer.
func requestToken(ctx context.Context, serverURL, username, password string) (string, error) {
	c, err := apiclient.New(serverURL, "")
	if err != nil {
		return "", err
	}
	resp, err := c.IssueTokenWithResponse(ctx, apiclient.TokenRequest{
		Username: username,
		Password: password,
	})
	if err != nil {
		return "", fmt.Errorf("posting to %s/auth/token: %w", serverURL, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return "", fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200 == nil || resp.JSON200.AccessToken == nil {
		return "", fmt.Errorf("server returned no access_token")
	}
	return *resp.JSON200.AccessToken, nil
}
