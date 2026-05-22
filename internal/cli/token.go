package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/config"
)

func newAuthCommand() *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication tokens.",
	}
	auth.AddCommand(newCreateTokenCommand())
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

// requestToken posts credentials to /auth/token and returns the access token.
func requestToken(ctx context.Context, serverURL, username, password string) (string, error) {
	payload, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return "", fmt.Errorf("encoding credentials: %w", err)
	}
	url := strings.TrimRight(serverURL, "/") + "/auth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("posting to %s: %w", url, err)
	}
	raw, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return "", fmt.Errorf("reading response: %w", readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("closing response: %w", closeErr)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server returned %d: %s", resp.StatusCode, string(raw))
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}
	return body.AccessToken, nil
}
