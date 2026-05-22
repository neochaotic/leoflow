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

// newRunsCommand groups the commands that trigger and inspect DAG runs.
func newRunsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Trigger and inspect DAG runs.",
	}
	cmd.AddCommand(newRunsTriggerCommand(), newRunsStatusCommand())
	return cmd
}

func newRunsTriggerCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "trigger <dag_id>",
		Short: "Trigger a new run of a DAG.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := resolveServerURL(cmd, serverURL)
			if err != nil {
				return err
			}
			url := strings.TrimRight(base, "/") + "/api/v2/dags/" + args[0] + "/dagRuns"
			status, raw, err := apiRequest(cmdContext(cmd), http.MethodPost, url, token, []byte("{}"))
			if err != nil {
				return err
			}
			if status >= http.StatusMultipleChoices {
				return fmt.Errorf("server returned %d: %s", status, raw)
			}
			var r struct {
				DagRunID string `json:"dag_run_id"`
				State    string `json:"state"`
			}
			if jerr := json.Unmarshal(raw, &r); jerr != nil {
				return fmt.Errorf("parsing response: %w", jerr)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Triggered run %s for %q (state %s)\n", r.DagRunID, args[0], r.State)
			return err
		},
	}
	addRunsFlags(cmd, &serverURL, &token)
	return cmd
}

func newRunsStatusCommand() *cobra.Command {
	var serverURL, token, runID string
	cmd := &cobra.Command{
		Use:   "status <dag_id>",
		Short: "Show the state of a DAG run (the latest by default).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := resolveServerURL(cmd, serverURL)
			if err != nil {
				return err
			}
			state, id, err := fetchRunState(cmdContext(cmd), base, token, args[0], runID)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", id, state)
			return err
		},
	}
	addRunsFlags(cmd, &serverURL, &token)
	cmd.Flags().StringVar(&runID, "run", "", "specific dag_run_id (default: the most recent run)")
	return cmd
}

// fetchRunState returns the state and id of the named run, or of the most recent
// run when runID is empty.
func fetchRunState(ctx context.Context, base, token, dagID, runID string) (state, id string, err error) {
	root := strings.TrimRight(base, "/") + "/api/v2/dags/" + dagID + "/dagRuns"
	if runID != "" {
		return decodeRun(ctx, root+"/"+runID, token)
	}
	status, raw, err := apiRequest(ctx, http.MethodGet, root, token, nil)
	if err != nil {
		return "", "", err
	}
	if status >= http.StatusMultipleChoices {
		return "", "", fmt.Errorf("server returned %d: %s", status, raw)
	}
	var list struct {
		DagRuns []struct {
			DagRunID string `json:"dag_run_id"`
			State    string `json:"state"`
		} `json:"dag_runs"`
	}
	if jerr := json.Unmarshal(raw, &list); jerr != nil {
		return "", "", fmt.Errorf("parsing response: %w", jerr)
	}
	if len(list.DagRuns) == 0 {
		return "", "", fmt.Errorf("no runs found for %q", dagID)
	}
	return list.DagRuns[0].State, list.DagRuns[0].DagRunID, nil
}

func decodeRun(ctx context.Context, url, token string) (state, id string, err error) {
	status, raw, err := apiRequest(ctx, http.MethodGet, url, token, nil)
	if err != nil {
		return "", "", err
	}
	if status >= http.StatusMultipleChoices {
		return "", "", fmt.Errorf("server returned %d: %s", status, raw)
	}
	var r struct {
		DagRunID string `json:"dag_run_id"`
		State    string `json:"state"`
	}
	if jerr := json.Unmarshal(raw, &r); jerr != nil {
		return "", "", fmt.Errorf("parsing response: %w", jerr)
	}
	return r.State, r.DagRunID, nil
}

func addRunsFlags(cmd *cobra.Command, serverURL, token *string) {
	cmd.Flags().StringVar(serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token")
}

// resolveServerURL returns the explicit --server value or the configured one.
func resolveServerURL(cmd *cobra.Command, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	cfg, err := config.Load(configFilePath(cmd), cmd.Flags())
	if err != nil {
		return "", err
	}
	return cfg.ServerURL, nil
}

// apiRequest performs a JSON HTTP request to the control plane and returns the
// status code and raw body.
func apiRequest(ctx context.Context, method, url, token string, body []byte) (status int, raw []byte, err error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("requesting %s: %w", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing response: %w", cerr)
		}
	}()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp.StatusCode, nil, fmt.Errorf("reading response: %w", readErr)
	}
	return resp.StatusCode, data, nil
}
