package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/config"
)

// newDagsCommand groups DAG-management subcommands.
func newDagsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dags",
		Short: "Manage registered DAGs.",
	}
	cmd.AddCommand(newDagsDeleteCommand())
	return cmd
}

// newDagsDeleteCommand clears a DAG's history or deregisters it (ADR 0020).
func newDagsDeleteCommand() *cobra.Command {
	var serverURL, token string
	var deregister bool
	cmd := &cobra.Command{
		Use:   "delete <dag_id>",
		Short: "Clear a DAG's run history, or fully deregister it with --deregister.",
		Long: "By default this clears the DAG's run history but keeps the DAG and its " +
			"versions registered — the same as the UI trash button (ADR 0020). With " +
			"--deregister it removes the DAG artifact entirely.\n\n" +
			"GitOps note: deregister is not permanent while the DAG's source still exists. " +
			"In production the next deploy re-registers it as a new version; under " +
			"`leoflow dev` the watcher re-registers it on the next reload — delete the " +
			"DAG's file to stop that.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dagID := args[0]
			if serverURL == "" {
				cfg, cerr := config.Load(configFilePath(cmd), cmd.Flags())
				if cerr != nil {
					return cerr
				}
				serverURL = cfg.ServerURL
			}
			status, body, err := deleteDag(cmdContext(cmd), serverURL, token, dagID, deregister)
			if err != nil {
				return err
			}
			if status >= http.StatusMultipleChoices {
				return fmt.Errorf("server returned %d: %s", status, body)
			}
			out := cmd.OutOrStdout()
			if deregister {
				if _, werr := fmt.Fprintf(out, "Deregistered %q (artifact removed).\n", dagID); werr != nil {
					return werr
				}
				_, werr := fmt.Fprintln(out, "Note: deregister is not permanent while the source exists — in prod the next "+
					"deploy re-registers it; under `leoflow dev` the next reload does, unless you delete the file.")
				return werr
			}
			_, werr := fmt.Fprintf(out, "Cleared %q's run history (DAG still registered). Use --deregister to remove the artifact.\n", dagID)
			return werr
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token")
	cmd.Flags().BoolVar(&deregister, "deregister", false, "remove the DAG artifact entirely, not just its run history")
	return cmd
}

// deleteDag calls DELETE /api/v2/dags/{id}, adding ?deregister=true to remove the
// artifact rather than only clearing history (ADR 0020).
func deleteDag(ctx context.Context, serverURL, token, dagID string, deregister bool) (status int, body string, err error) {
	u := strings.TrimRight(serverURL, "/") + "/api/v2/dags/" + url.PathEscape(dagID)
	if deregister {
		u += "?deregister=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, http.NoBody)
	if err != nil {
		return 0, "", fmt.Errorf("building request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("calling %s: %w", u, err)
	}
	raw, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return resp.StatusCode, "", fmt.Errorf("reading response: %w", readErr)
	}
	if closeErr != nil {
		return resp.StatusCode, "", fmt.Errorf("closing response: %w", closeErr)
	}
	return resp.StatusCode, string(raw), nil
}
