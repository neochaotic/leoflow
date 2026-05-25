package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	if oerr := overlayProject(o.output, cfg); oerr != nil {
		return oerr
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
	// Stream the parser's stderr to the terminal and capture it, so a parse
	// failure carries the real traceback (e.g. the SyntaxError + file:line) in
	// the returned error — surfaced both in the dev terminal and the UI's import
	// error banner, not just an opaque "exit status 1".
	var stderr bytes.Buffer
	pc.Stderr = io.MultiWriter(cmd.ErrOrStderr(), &stderr)
	if err := pc.Run(); err != nil {
		if detail := lastLines(strings.TrimSpace(stderr.String()), 20); detail != "" {
			return fmt.Errorf("running parser %q: %w\n%s", command, err, detail)
		}
		return fmt.Errorf("running parser %q: %w", command, err)
	}
	return nil
}

// lastLines returns the final n lines of s (the most relevant tail of a Python
// traceback), trimming the rest so the error stays bounded.
func lastLines(s string, n int) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// overlayProject writes the leoflow.yaml Leoflow-specific config (staging and
// per-task overrides) onto the produced dag.json. These are deployment concerns,
// not Airflow DAG attributes, so the parser does not emit them (ADR 0022, 0023).
// Per-task overrides are bound by task_id; an entry naming a task absent from the
// DAG is a hard error (no silent drop). No-op when nothing is configured.
func overlayProject(dagJSONPath string, cfg *domain.LeoflowConfig) error {
	if cfg.Staging == nil && len(cfg.Tasks) == 0 {
		return nil
	}
	data, err := os.ReadFile(dagJSONPath) //nolint:gosec // G304: output path is operator-supplied on the CLI.
	if err != nil {
		return fmt.Errorf("reading %s: %w", dagJSONPath, err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		return fmt.Errorf("parsing %s: %w", dagJSONPath, uerr)
	}
	if cfg.Staging != nil {
		spec.Staging = cfg.Staging
	}
	if verr := validateTaskBindings(cfg.Tasks, spec.Tasks); verr != nil {
		return verr
	}
	for i := range spec.Tasks {
		if override := cfg.Tasks[spec.Tasks[i].TaskID]; override != nil {
			applyTaskOverride(&spec.Tasks[i], override)
		}
	}
	out, err := json.MarshalIndent(&spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", dagJSONPath, err)
	}
	if werr := os.WriteFile(dagJSONPath, append(out, '\n'), 0o600); werr != nil {
		return fmt.Errorf("writing %s: %w", dagJSONPath, werr)
	}
	return nil
}

// validateTaskBindings guards the YAML↔task binding: every key in the leoflow.yaml
// tasks block must name a task_id present in the compiled DAG, so a typo fails the
// compile instead of silently overriding nothing (ADR 0023).
func validateTaskBindings(overrides map[string]*domain.TaskConfig, tasks []domain.TaskSpec) error {
	if len(overrides) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(tasks))
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		known[t.TaskID] = struct{}{}
		ids = append(ids, t.TaskID)
	}
	for id := range overrides {
		if _, ok := known[id]; !ok {
			sort.Strings(ids)
			return fmt.Errorf("leoflow.yaml tasks: unknown task_id %q; the DAG defines %v", id, ids)
		}
	}
	return nil
}

// applyTaskOverride sets each override field that is present onto the task,
// leaving unset fields as compiled. Env entries are merged over any existing env.
func applyTaskOverride(task *domain.TaskSpec, o *domain.TaskConfig) {
	if o.Retries != nil {
		task.Retries = o.Retries
	}
	if o.RetryDelaySeconds != nil {
		task.RetryDelaySeconds = o.RetryDelaySeconds
	}
	if o.ExecutionTimeoutSeconds != nil {
		task.ExecutionTimeoutSeconds = o.ExecutionTimeoutSeconds
	}
	if o.Resources != nil {
		task.Resources = o.Resources
	}
	if o.Execution != nil {
		task.Execution = o.Execution
	}
	if len(o.Env) > 0 {
		if task.Env == nil {
			task.Env = make(map[string]string, len(o.Env))
		}
		for k, v := range o.Env {
			task.Env[k] = v
		}
	}
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
