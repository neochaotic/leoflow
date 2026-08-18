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
	auth.AddCommand(newCreateUserCommand())
	return auth
}

// newCreateUserCommand builds `leoflow auth create-user`: it creates an account
// on the control plane via the admin-only POST /api/v2/users endpoint. It is the
// long-promised counterpart to bootstrap (ADR 0008) — until now the only way to
// mint a user was the Lite bootstrap admin. Unlike create-token/login (which
// exchange credentials for a token), this call is privileged, so it carries an
// existing admin's bearer token, resolved with the same precedence as `deploy`:
// --token, then LEOFLOW_TOKEN, then the session saved by `auth login`.
func newCreateUserCommand() *cobra.Command {
	var serverURL, token, email, password, role string
	cmd := &cobra.Command{
		Use:   "create-user",
		Short: "Create a user on the control plane (admin only).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedServer, resolvedToken, rerr := resolveServerToken(cmd, serverURL, token)
			if rerr != nil {
				return rerr
			}
			if email == "" || password == "" {
				return fmt.Errorf("--email and --password are required")
			}
			created, err := createUser(cmdContext(cmd), resolvedServer, resolvedToken, email, password, role)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created user %s (id %s, role %s)\n",
				created.Email, created.Id, roleOrNone(created.Role))
			return err
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "admin JWT bearer token (default: config token)")
	cmd.Flags().StringVar(&email, "email", "", "email of the user to create")
	cmd.Flags().StringVar(&password, "password", os.Getenv("LEOFLOW_PASSWORD"), "password for the new user")
	cmd.Flags().StringVar(&role, "role", "", "existing role to grant (e.g. admin); empty grants none")
	return cmd
}

// createUser posts to /api/v2/users through the shared typed client, carrying the
// admin bearer, and returns the created user. It mirrors requestToken's use of
// the generated client (ADR 0050 D8) rather than hand-rolling HTTP.
func createUser(ctx context.Context, serverURL, token, email, password, role string) (apiclient.User, error) {
	c, err := apiclient.New(serverURL, token)
	if err != nil {
		return apiclient.User{}, err
	}
	body := apiclient.CreateUserRequest{Email: email, Password: &password}
	if role != "" {
		body.Role = &role
	}
	resp, err := c.CreateUserWithResponse(ctx, body)
	if err != nil {
		return apiclient.User{}, fmt.Errorf("posting to %s/api/v2/users: %w", serverURL, err)
	}
	if resp.StatusCode() != http.StatusCreated {
		return apiclient.User{}, fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON201 == nil {
		return apiclient.User{}, fmt.Errorf("server returned no user")
	}
	return *resp.JSON201, nil
}

// roleOrNone renders an optional role for CLI output, showing "(none)" when the
// user was created without one.
func roleOrNone(role *string) string {
	if role == nil || *role == "" {
		return "(none)"
	}
	return *role
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
