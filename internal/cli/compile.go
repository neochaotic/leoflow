package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/domain"
)

// compileOptions holds the resolved flags for a compile run.
type compileOptions struct {
	output     string
	image      string
	parserCmd  string
	dagVersion string
	builder    string
	dockerfile string
	build      bool
	push       bool
}

func newCompileCommand() *cobra.Command {
	var o compileOptions
	cmd := &cobra.Command{
		Use:   "compile [path]",
		Short: "Compile a DAG project into dag.json via the Python parser.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runCompile(cmd, dir, o)
		},
	}
	cmd.Flags().StringVarP(&o.output, "output", "o", "dag.json", "path to write the compiled dag.json")
	cmd.Flags().StringVar(&o.image, "image", "", "container image reference for the DAG")
	cmd.Flags().StringVar(&o.parserCmd, "parser-cmd", "", "override the parser command (default from config)")
	cmd.Flags().StringVar(&o.dagVersion, "dag-version", "", "DAG version label (default: git describe, else dev)")
	cmd.Flags().BoolVar(&o.build, "build", false, "build the DAG container image (requires --image)")
	cmd.Flags().BoolVar(&o.push, "push", false, "push the built image to its registry (requires --build)")
	cmd.Flags().StringVar(&o.builder, "builder", "docker", "image build tool to shell out to (e.g. docker, podman, nerdctl)")
	cmd.Flags().StringVar(&o.dockerfile, "dockerfile", "Dockerfile", "Dockerfile path relative to the DAG directory")
	return cmd
}

// runCompile resolves the project config, runs the parser, validates the output,
// and optionally builds the DAG image.
func runCompile(cmd *cobra.Command, dir string, o compileOptions) error {
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		return err
	}
	if verr := cfg.Validate(); verr != nil {
		return fmt.Errorf("invalid %s: %w", projectConfigPath(dir), verr)
	}
	command, err := resolveParserCommand(cmd, o.parserCmd)
	if err != nil {
		return err
	}
	if o.dagVersion == "" {
		o.dagVersion = gitVersion(cmdContext(cmd))
	}
	if ierr := checkImageFlags(cmd, o.build, o.push, o.image); ierr != nil {
		return ierr
	}
	if rerr := runParser(cmd, command, parserArgs{
		source:     dagSourcePath(dir, cfg),
		config:     projectConfigPath(dir),
		output:     o.output,
		image:      o.image,
		dagVersion: o.dagVersion,
	}); rerr != nil {
		return rerr
	}
	if verr := validateDAGFile(o.output); verr != nil {
		return verr
	}
	if eerr := embedSource(o.output, dagSourcePath(dir, cfg)); eerr != nil {
		return eerr
	}
	if o.build {
		if berr := buildImage(cmd, o.builder, o.image, filepath.Join(dir, o.dockerfile), dir); berr != nil {
			return berr
		}
	}
	if o.push {
		if perr := pushImage(cmd, o.builder, o.image); perr != nil {
			return perr
		}
	}
	_, werr := fmt.Fprintf(cmd.OutOrStdout(), "Compiled %s -> %s (image %s, version %s)\n", dagSourcePath(dir, cfg), o.output, o.image, o.dagVersion)
	return werr
}

// checkImageFlags enforces the --build/--image relationship and notes when an
// image is recorded without being built.
func checkImageFlags(cmd *cobra.Command, build, push bool, image string) error {
	if push && !build {
		return errors.New("--push requires --build")
	}
	if build && image == "" {
		return errors.New("--build requires --image")
	}
	if !build && image != "" {
		_, werr := fmt.Fprintf(cmd.ErrOrStderr(), "note: recording image %q without building it; pass --build to build the DAG image\n", image)
		return werr
	}
	return nil
}

// buildImage shells out to the configured builder to build the DAG image
// out-of-process (ADR 0015: no Docker SDK in our binaries). The build context
// is the DAG directory.
func buildImage(cmd *cobra.Command, builder, image, dockerfile, contextDir string) error {
	//nolint:gosec // G204: builder is operator-configured by design (ADR 0015).
	bc := exec.CommandContext(cmdContext(cmd), builder, "build", "-t", image, "-f", dockerfile, contextDir)
	bc.Stdout = cmd.OutOrStdout()
	bc.Stderr = cmd.ErrOrStderr()
	if err := bc.Run(); err != nil {
		return fmt.Errorf("building image %q with %q: %w", image, builder, err)
	}
	return nil
}

// pushImage shells out to the configured builder to push the image to its
// registry (out-of-process; ADR 0015).
func pushImage(cmd *cobra.Command, builder, image string) error {
	//nolint:gosec // G204: builder is operator-configured by design (ADR 0015).
	pc := exec.CommandContext(cmdContext(cmd), builder, "push", image)
	pc.Stdout = cmd.OutOrStdout()
	pc.Stderr = cmd.ErrOrStderr()
	if err := pc.Run(); err != nil {
		return fmt.Errorf("pushing image %q with %q: %w", image, builder, err)
	}
	return nil
}

// parserArgs collects the inputs passed to the Python parser subprocess.
type parserArgs struct {
	source     string
	config     string
	output     string
	image      string
	dagVersion string
}

// gitVersion derives a version label from git, falling back to "dev".
func gitVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return "dev"
	}
	return strings.TrimSpace(string(out))
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
		"--image", a.image,
		"--dag-version", a.dagVersion)
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
	if err := spec.ValidateInlineExecution(domain.DefaultInlineMaxDurationSeconds); err != nil {
		return fmt.Errorf("produced %s is invalid: %w", path, err)
	}
	return nil
}

// embedSource reads the produced dag.json and the original dag.py and stores the
// Python text in the spec's source field, so the control plane can serve it to
// the UI's Code tab. Re-marshaled with indentation to keep dag.json readable.
func embedSource(dagJSONPath, sourcePath string) error {
	specData, err := os.ReadFile(dagJSONPath) //nolint:gosec // G304: output path is operator-supplied on the CLI.
	if err != nil {
		return fmt.Errorf("reading %s: %w", dagJSONPath, err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(specData, &spec); uerr != nil {
		return fmt.Errorf("parsing %s: %w", dagJSONPath, uerr)
	}
	src, err := os.ReadFile(sourcePath) //nolint:gosec // G304: source path is operator-supplied on the CLI.
	if err != nil {
		return fmt.Errorf("reading dag source %s: %w", sourcePath, err)
	}
	spec.Source = string(src)
	out, err := json.MarshalIndent(&spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", dagJSONPath, err)
	}
	if werr := os.WriteFile(dagJSONPath, append(out, '\n'), 0o600); werr != nil {
		return fmt.Errorf("writing %s: %w", dagJSONPath, werr)
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
