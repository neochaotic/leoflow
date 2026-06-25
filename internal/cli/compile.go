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
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/dbt"
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
	if cfg.Dbt != nil {
		return runDbtCompile(cmd, dir, o, cfg)
	}
	command, err := resolveParserCommand(cmd, o.parserCmd)
	if err != nil {
		return err
	}
	if o.dagVersion == "" {
		o.dagVersion = gitVersion(cmdContext(cmd))
	}
	// The image is the --image flag when set, else derived from the leoflow.yaml
	// registry block (url/image_name:version), so a yaml-driven build needs no
	// flag. The resolved value flows into dag.json (via the parser) and the build,
	// keeping the registered artifact and the built/pushed image in lockstep.
	image := resolveBuildImage(o.image, cfg, o.dagVersion)
	if ierr := checkImageFlags(cmd, o.build, o.push, image); ierr != nil {
		return ierr
	}
	if rerr := runParser(cmd, command, parserArgs{
		source:        dagSourcePath(dir, cfg),
		config:        projectConfigPath(dir),
		output:        o.output,
		image:         image,
		dagVersion:    o.dagVersion,
		projectConfig: cfg,
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
	if perr := validateOperatorProvidersFile(o.output, cfg); perr != nil {
		return perr
	}
	if berr := buildAndPush(cmd, dir, o, cfg, image); berr != nil {
		return berr
	}
	_, werr := fmt.Fprint(cmd.OutOrStdout(), compileSummary(dagSourcePath(dir, cfg), o.output, image, o.dagVersion))
	return werr
}

// runDbtCompile compiles a dbt project (ADR 0042) instead of a Python DAG: it
// acquires the manifest.json (a baked file or a fresh `dbt parse`), renders it
// into a dag.json via the dbt package, overlays the leoflow.yaml, validates, and
// optionally builds/pushes the image — reusing the same tail as the parser path.
func runDbtCompile(cmd *cobra.Command, dir string, o compileOptions, cfg *domain.LeoflowConfig) error {
	if o.dagVersion == "" {
		o.dagVersion = gitVersion(cmdContext(cmd))
	}
	image := resolveBuildImage(o.image, cfg, o.dagVersion)
	if ierr := checkImageFlags(cmd, o.build, o.push, image); ierr != nil {
		return ierr
	}
	manifest, err := loadDbtManifest(cmd, dir, cfg.Dbt)
	if err != nil {
		return err
	}
	spec, err := dbt.Compile(manifest, dbt.Meta{
		DagID:       cfg.DagID,
		DagVersion:  o.dagVersion,
		Image:       image,
		Schedule:    cfg.Dbt.Schedule,
		Granularity: dbt.Granularity(cfg.Dbt.Granularity),
	})
	if err != nil {
		return fmt.Errorf("dbt compile: %w", err)
	}
	if werr := writeDAGFile(o.output, &spec); werr != nil {
		return werr
	}
	if oerr := overlayProject(o.output, cfg); oerr != nil {
		return oerr
	}
	if verr := validateDAGFile(o.output); verr != nil {
		return verr
	}
	if berr := buildAndPush(cmd, dir, o, cfg, image); berr != nil {
		return berr
	}
	_, werr := fmt.Fprint(cmd.OutOrStdout(), compileSummary(filepath.Join(dir, cfg.Dbt.Project), o.output, image, o.dagVersion))
	return werr
}

// loadDbtManifest returns the dbt manifest.json bytes: a pre-built file when
// dbt.manifest is set (the Pro/CI baked path), else the output of running
// `dbt parse` in the project directory (the Lite path).
func loadDbtManifest(cmd *cobra.Command, dir string, c *domain.DbtConfig) ([]byte, error) {
	if c.Manifest != "" {
		path := filepath.Join(dir, c.Manifest)
		data, rerr := os.ReadFile(path) //nolint:gosec // G304: operator-supplied project path.
		if rerr != nil {
			return nil, fmt.Errorf("reading dbt manifest %s: %w", path, rerr)
		}
		return data, nil
	}
	projectDir := filepath.Join(dir, c.Project)
	pc := exec.CommandContext(cmdContext(cmd), "dbt", "parse")
	pc.Dir = projectDir
	pc.Stderr = cmd.ErrOrStderr()
	if rerr := pc.Run(); rerr != nil {
		return nil, fmt.Errorf("dbt parse in %s: %w", projectDir, rerr)
	}
	path := filepath.Join(projectDir, "target", "manifest.json")
	data, rerr := os.ReadFile(path) //nolint:gosec // G304: derived from operator-supplied project path.
	if rerr != nil {
		return nil, fmt.Errorf("reading dbt manifest %s: %w", path, rerr)
	}
	return data, nil
}

// writeDAGFile marshals a DAGSpec to path as indented dag.json.
func writeDAGFile(path string, spec *domain.DAGSpec) error {
	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding dag.json: %w", err)
	}
	if werr := os.WriteFile(path, append(out, '\n'), 0o600); werr != nil {
		return fmt.Errorf("writing %s: %w", path, werr)
	}
	return nil
}

// buildAndPush optionally builds the DAG image and pushes it, honoring the
// --build/--push flags. When building and the project ships no Dockerfile, one is
// generated from the leoflow.yaml (base image + system packages + dependencies/
// connectors), built, and removed afterward so the workspace stays clean.
func buildAndPush(cmd *cobra.Command, dir string, o compileOptions, cfg *domain.LeoflowConfig, image string) error {
	if o.build {
		name := resolveDockerfileName(cmd, o, cfg)
		dockerfile, cleanup, derr := ensureDockerfile(dir, name, cfg, dagSourcePath(dir, cfg))
		if derr != nil {
			return derr
		}
		defer cleanup()
		var platforms []string
		if cfg.Build != nil {
			platforms = cfg.Build.Platforms
		}
		if berr := buildImage(cmd, o.builder, image, dockerfile, dir, platforms); berr != nil {
			return berr
		}
	}
	if o.push {
		if perr := pushImage(cmd, o.builder, image); perr != nil {
			return perr
		}
	}
	return nil
}

// resolveDockerfileName picks the Dockerfile name to look for in the DAG
// directory: an explicit --dockerfile flag wins, then leoflow.yaml's
// build.dockerfile, else the "Dockerfile" default.
func resolveDockerfileName(cmd *cobra.Command, o compileOptions, cfg *domain.LeoflowConfig) string {
	if cmd.Flags().Changed("dockerfile") {
		return o.dockerfile
	}
	if cfg.Build != nil && cfg.Build.Dockerfile != "" {
		return cfg.Build.Dockerfile
	}
	return o.dockerfile
}

// compileSummary formats the success line for `leoflow compile`. When image is
// empty it renders `no image` instead of `image ` to avoid the dangling-comma
// artifact reported as #D10 in the dogfood audit (#212).
func compileSummary(src, out, image, version string) string {
	imageField := "image " + image
	if image == "" {
		imageField = "no image"
	}
	return fmt.Sprintf("Compiled %s -> %s (%s, version %s)\n", src, out, imageField, version)
}

// checkImageFlags enforces the --build/--image relationship and notes when an
// image is recorded without being built.
func checkImageFlags(cmd *cobra.Command, build, push bool, image string) error {
	if push && !build {
		return errors.New("--push requires --build")
	}
	if build && image == "" {
		return errors.New("--build needs an image reference: pass --image, or set registry.url + registry.image_name in leoflow.yaml")
	}
	if !build && image != "" {
		_, werr := fmt.Fprintf(cmd.ErrOrStderr(), "note: recording image %q without building it; pass --build to build the DAG image\n", image)
		return werr
	}
	return nil
}

// buildArgs assembles the builder argv for a DAG image build. When platforms is
// non-empty it injects `--platform a,b`, so a deploy can target the cluster's
// architecture instead of the host's — the Mac-safe default linux/amd64 (ADR
// 0041). A single platform works with a plain `docker build`; a multi-platform
// list is the caller's signal to route through buildx (the deploy command does).
func buildArgs(image, dockerfile, contextDir string, platforms []string) []string {
	args := []string{"build"}
	if len(platforms) > 0 {
		args = append(args, "--platform", strings.Join(platforms, ","))
	}
	return append(args, "-t", image, "-f", dockerfile, contextDir)
}

// buildImage shells out to the configured builder to build the DAG image
// out-of-process (ADR 0015: no Docker SDK in our binaries). The build context
// is the DAG directory; platforms selects the target architecture(s).
func buildImage(cmd *cobra.Command, builder, image, dockerfile, contextDir string, platforms []string) error {
	//nolint:gosec // G204: builder is operator-configured by design (ADR 0015).
	bc := exec.CommandContext(cmdContext(cmd), builder, buildArgs(image, dockerfile, contextDir, platforms)...)
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
	// projectConfig is the parsed + defaulted LeoflowConfig the CLI loaded
	// before invoking the parser. It is marshaled to JSON and handed to the
	// parser via LEOFLOW_PROJECT_CONFIG_JSON, replacing the in-parser
	// PyYAML read of leoflow.yaml. The Go side stays the single source of
	// truth for the config schema; the parser carries zero third-party
	// Python deps (ADR 0024 + alpha cleanup).
	projectConfig *domain.LeoflowConfig
}

// parserConfigEnv is the env var that carries the resolved project config
// (JSON) from the CLI to the Python parser. The parser refuses to read a
// YAML file directly — keeping this seam tight prevents a re-vendoring of
// PyYAML.
const parserConfigEnv = "LEOFLOW_PROJECT_CONFIG_JSON"

// gitVersion derives a version label from git, falling back to "dev".
func gitVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return "dev"
	}
	return strings.TrimSpace(string(out))
}

// gitSHA returns the short commit hash, or "" when the project is not in git —
// so a deploy with tag_strategy git_sha gets a genuine per-commit tag and falls
// back to the version label otherwise.
func gitSHA(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
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
	// Hand the resolved project config to the parser as JSON via an env var.
	// The parser uses this instead of re-parsing leoflow.yaml in-process, so
	// Go owns the schema and the parser ships zero third-party deps.
	if a.projectConfig != nil {
		raw, merr := json.Marshal(a.projectConfig)
		if merr != nil {
			return fmt.Errorf("marshaling project config for parser: %w", merr)
		}
		pc.Env = append(os.Environ(), parserConfigEnv+"="+string(raw))
	}
	pc.Stdout = cmd.OutOrStdout()
	// Stream the parser's stderr to the terminal and capture it, so a parse
	// failure carries the real traceback (e.g. the SyntaxError + file:line) in
	// the returned error — surfaced both in the dev terminal and the UI's import
	// error banner, not just an opaque "exit status 1".
	var stderr bytes.Buffer
	pc.Stderr = io.MultiWriter(cmd.ErrOrStderr(), &stderr)
	if err := pc.Run(); err != nil {
		raw := strings.TrimSpace(stderr.String())
		detail := lastLines(raw, 20)
		// Lead with the user-actionable summary line (e.g. `SyntaxError: ...`)
		// when present, so the cause is visible above the bounded traceback
		// instead of buried under our internal parser file paths (#D9).
		if summary := parserErrorSummary(raw); summary != "" {
			if detail != "" {
				return fmt.Errorf("%s\n\n%s", summary, detail)
			}
			return errors.New(summary)
		}
		if detail != "" {
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

// pythonErrorRe matches a single Python exception-summary line of the form
// `<CamelCaseName>Error: <message>` — the canonical last line of a traceback.
// The leading-uppercase + ending-in-Error shape filters out the noisy
// `File "..."` and code-snippet lines that surround it.
var pythonErrorRe = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*Error: .+$`)

// parserErrorSummary extracts the deepest Python exception summary
// (`*Error: message`) from a parser traceback. The deepest line is the
// actual cause; intermediate "During handling of the above exception ..."
// wrappers are skipped. Returns "" when no error line is present so callers
// can fall back to the raw traceback. Closes #D9 from the dogfood audit
// (#212): users see a one-line cause instead of an internal-paths dump.
func parserErrorSummary(stderr string) string {
	var last string
	for _, raw := range strings.Split(stderr, "\n") {
		ln := strings.TrimSpace(raw)
		if pythonErrorRe.MatchString(ln) {
			last = ln
		}
	}
	return last
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
	if err := spec.ValidateSchedule(); err != nil {
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
