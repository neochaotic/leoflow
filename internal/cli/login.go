package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/config"
)

// newLoginCommand builds `leoflow login`: it exchanges credentials for a JWT at
// the control plane and persists the token (and server URL) to the config file,
// so subsequent `push`/`deploy` calls need no auth flags. This is what makes the
// pipeline-less Lite->Pro loop fluid (login once, deploy many) — ADR 0041.
func newLoginCommand() *cobra.Command {
	var serverURL, username, password string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate to a control plane and store the token.",
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
			path, perr := sessionConfigPath(cmd)
			if perr != nil {
				return perr
			}
			if serr := config.PersistSession(path, serverURL, token); serr != nil {
				return serr
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Logged in to %s (token saved to %s)\n", serverURL, path)
			return err
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&username, "username", os.Getenv("LEOFLOW_USERNAME"), "username")
	cmd.Flags().StringVar(&password, "password", os.Getenv("LEOFLOW_PASSWORD"), "password")
	return cmd
}

// sessionConfigPath resolves the config file `login` writes to: the --config
// flag when set, otherwise the default ~/.leoflow/config.yaml. Unlike
// configFilePath, it returns the default path even when the file does not yet
// exist, because login is allowed to create it.
func sessionConfigPath(cmd *cobra.Command) (string, error) {
	if p, err := cmd.Flags().GetString("config"); err == nil && strings.TrimSpace(p) != "" {
		return p, nil
	}
	return config.DefaultConfigFile()
}
