package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/domain"
)

func newCompileCommand() *cobra.Command {
	var output, image, parserCmd string
	cmd := &cobra.Command{
		Use:   "compile [path]",
		Short: "Compile a DAG project into dag.json via the Python parser.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			cfg, err := loadProjectConfig(dir)
			if err != nil {
				return err
			}
			if verr := cfg.Validate(); verr != nil {
				return fmt.Errorf("invalid %s: %w", projectConfigPath(dir), verr)
			}

			command, err := resolveParserCommand(cmd, parserCmd)
			if err != nil {
				return err
			}
			if rerr := runParser(cmd, command, parserArgs{
				source: dagSourcePath(dir, cfg),
				config: projectConfigPath(dir),
				output: output,
				image:  image,
			}); rerr != nil {
				return rerr
			}
			if verr := validateDAGFile(output); verr != nil {
				return verr
			}
			if _, werr := fmt.Fprintf(cmd.OutOrStdout(), "Compiled %s -> %s (image %s)\n", dagSourcePath(dir, cfg), output, image); werr != nil {
				return werr
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "dag.json", "path to write the compiled dag.json")
	cmd.Flags().StringVar(&image, "image", "", "container image reference for the DAG")
	cmd.Flags().StringVar(&parserCmd, "parser-cmd", "", "override the parser command (default from config)")
	return cmd
}

// parserArgs collects the inputs passed to the Python parser subprocess.
type parserArgs struct {
	source string
	config string
	output string
	image  string
}

// resolveParserCommand returns the explicit --parser-cmd value when set,
// otherwise the command resolved from configuration.
func resolveParserCommand(cmd *cobra.Command, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	cfg, err := config.Load(configFilePath(cmd), cmd.Flags())
	if err != nil {
		return "", err
	}
	return cfg.ParserCmd, nil
}

// runParser invokes the operator-configured parser command with the compile
// subcommand and its arguments, streaming output to the command's streams.
func runParser(cmd *cobra.Command, command string, a parserArgs) error {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return errors.New("parser command is empty")
	}
	argv := make([]string, 0, len(fields)+8)
	argv = append(argv, fields[1:]...)
	argv = append(argv, "compile",
		"--source", a.source,
		"--config", a.config,
		"--output", a.output,
		"--image", a.image)
	//nolint:gosec // G204: the parser command is operator-configured by design (ADR 0005).
	pc := exec.CommandContext(cmdContext(cmd), fields[0], argv...)
	pc.Stdout = cmd.OutOrStdout()
	pc.Stderr = cmd.ErrOrStderr()
	if err := pc.Run(); err != nil {
		return fmt.Errorf("running parser %q: %w", command, err)
	}
	return nil
}

// validateDAGFile reads a produced dag.json and validates it against the schema.
func validateDAGFile(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: output path is supplied by the operator on the CLI.
	if err != nil {
		return fmt.Errorf("reading produced %s: %w", path, err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parsing produced %s: %w", path, err)
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("produced %s is invalid: %w", path, err)
	}
	return nil
}

// cmdContext returns the command's context, falling back to context.Background.
func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
