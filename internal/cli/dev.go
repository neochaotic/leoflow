package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

// devEnv is the fixed local-development environment label and its defaults. The
// subprocess executor runs user code unsandboxed, so `leoflow dev` is dev-only
// and shouts that fact in the banner and the UI navbar (ADR 0023).
const (
	devInstanceName = "Leoflow · DEV"
	// devDatabaseURL targets a DEDICATED database, isolated from the product's
	// "leoflow" db so the dev experience never mixes data with product development
	// (no split brain). devMaintenanceURL is used only to CREATE it on first run.
	devDBName         = "leoflow_dev"
	devDatabaseURL    = "postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable"
	devMaintenanceURL = "postgres://leoflow:leoflow@localhost:5432/postgres?sslmode=disable"
	devRedisURL       = "redis://localhost:6379/0"
	// taskSDKVersion matches the task image (runtime/Dockerfile); the dev venv
	// installs it so dag.py's `from airflow.sdk import ...` resolves.
	taskSDKVersion  = "apache-airflow-task-sdk==1.2.1"
	devJWTSecret    = "dev-insecure-jwt-secret-change-me"
	devSecretKey    = "dev-insecure-secret-key-32bytes!"
	devAdminUser    = "admin@leoflow.local"
	devUIURL        = "http://localhost:8080"
	devPollInterval = 750 * time.Millisecond
	devReadyTimeout = 30 * time.Second
)

const (
	ansiReset = "\x1b[0m"
	ansiDev   = "\x1b[30;43m" // black text on a yellow background
)

// devOptions holds the resolved flags for a dev run.
type devOptions struct {
	image       string
	composeFile string
	migrations  string
	runtimeSrc  string
	serverBin   string
	agentBin    string
	noUp        bool
}

func newDevCommand() *cobra.Command {
	var o devOptions
	cmd := &cobra.Command{
		Use:   "dev [path]",
		Short: "Run the project locally with hot reload (no image build).",
		Long: "dev brings up local dependencies, runs the control plane with the " +
			"subprocess executor (user code runs UNSANDBOXED — dev only), registers the " +
			"DAG, and hot-reloads on every save. The Airflow UI is served at " + devUIURL +
			" and marked as the DEV environment. Run it from the leoflow source tree " +
			"(or point --migrations/--compose/--server-bin/--agent-bin elsewhere).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runDev(cmd, dir, o)
		},
	}
	cmd.Flags().StringVar(&o.image, "image", "leoflow-dev:local", "placeholder image recorded in dag.json (not built in dev)")
	cmd.Flags().StringVar(&o.composeFile, "compose", "docker-compose.dev.yaml", "compose file for local Postgres + Redis")
	cmd.Flags().StringVar(&o.migrations, "migrations", "migrations", "path to the SQL migrations directory")
	cmd.Flags().StringVar(&o.runtimeSrc, "runtime-src", "runtime/python", "source of the leoflow_runtime package installed into the dev venv")
	cmd.Flags().StringVar(&o.serverBin, "server-bin", "", "leoflow-server binary (default: PATH, then ./bin)")
	cmd.Flags().StringVar(&o.agentBin, "agent-bin", "", "leoflow-agent binary (default: PATH, then ./bin)")
	cmd.Flags().BoolVar(&o.noUp, "no-up", false, "skip docker compose (Postgres/Redis already running); the dev DB + venv are still provisioned")
	return cmd
}

// devPrintf writes progress output for the dev loop, discarding the unhelpful
// write error (output is a terminal in dev).
func devPrintf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...) //nolint:errcheck // best-effort terminal progress output
}

// devPrintln writes a progress line for the dev loop, discarding the write error.
func devPrintln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...) //nolint:errcheck // best-effort terminal progress output
}

// devBanner renders a high-visibility DEV-environment banner so a developer
// never mistakes the local loop for production. url is the served UI address.
func devBanner(url string) string {
	line := fmt.Sprintf(" LEOFLOW DEV — local, unsandboxed — %s ", url)
	bar := ""
	for range line {
		bar += "─"
	}
	return fmt.Sprintf("%s╭%s╮%s\n%s│%s│%s\n%s╰%s╯%s",
		ansiDev, bar, ansiReset,
		ansiDev, line, ansiReset,
		ansiDev, bar, ansiReset)
}

// projectMtimes returns the modtime of each existing path, silently skipping
// paths that do not exist so a not-yet-created file can be detected on creation.
func projectMtimes(paths []string) map[string]time.Time {
	out := make(map[string]time.Time, len(paths))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			out[p] = fi.ModTime()
		}
	}
	return out
}

// mtimesChanged reports whether any watched file appeared, vanished, or had its
// modtime move between two snapshots.
func mtimesChanged(prev, cur map[string]time.Time) bool {
	if len(prev) != len(cur) {
		return true
	}
	for p, t := range cur {
		if old, ok := prev[p]; !ok || !old.Equal(t) {
			return true
		}
	}
	return false
}

// runDev orchestrates the all-in-one local loop: deps, control plane (subprocess
// executor), DAG registration, and hot reload on save.
func runDev(cmd *cobra.Command, dir string, o devOptions) error {
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		return err
	}
	if verr := cfg.Validate(); verr != nil {
		return fmt.Errorf("invalid %s: %w", projectConfigPath(dir), verr)
	}
	out := cmd.OutOrStdout()
	devPrintln(out, devBanner(devUIURL))

	ctx, stop := signal.NotifyContext(cmdContext(cmd), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !o.noUp {
		if uerr := devComposeUp(ctx, cmd, o); uerr != nil {
			return uerr
		}
	}
	// Provision the isolated dev state: own database + own venv (never the
	// product's database or the system Python).
	if derr := ensureDevDatabase(ctx, cmd); derr != nil {
		return derr
	}
	if merr := devMigrate(ctx, cmd, o); merr != nil {
		return merr
	}
	home, herr := devHome()
	if herr != nil {
		return herr
	}
	venvPy, verr := ensureDevVenv(ctx, cmd, home, o.runtimeSrc, cfg.Dependencies)
	if verr != nil {
		return verr
	}
	serverBin, agentBin, berr := resolveDevBinaries(o)
	if berr != nil {
		return berr
	}
	workDir, aerr := filepath.Abs(dir)
	if aerr != nil {
		return fmt.Errorf("resolving project dir: %w", aerr)
	}
	server, serr := startDevServer(ctx, cmd, serverBin, agentBin, workDir, venvPy)
	if serr != nil {
		return serr
	}
	defer func() { _ = server.Process.Signal(syscall.SIGTERM) }() //nolint:errcheck // best-effort shutdown of the dev server

	if werr := waitForReady(ctx, devUIURL); werr != nil {
		return werr
	}
	// Mint an admin token in-process signed with the dev JWT secret; the control
	// plane validates it by signature + claims, so no login or seeded user is
	// needed (works against a fresh or pre-existing dev database).
	token, terr := auth.MintUserToken(devJWTSecret, time.Hour, auth.User{
		ID: "leoflow-dev", TenantID: "default", Email: devAdminUser, Roles: []string{"admin"},
	})
	if terr != nil {
		return fmt.Errorf("minting dev token: %w", terr)
	}
	return devWatchLoop(ctx, cmd, dir, cfg, o, token)
}

// devComposeUp starts local Postgres + Redis via docker compose (the shared
// server; the dev's own database lives inside it, isolated by name).
func devComposeUp(ctx context.Context, cmd *cobra.Command, o devOptions) error {
	devPrintln(cmd.OutOrStdout(), "▸ starting dependencies (docker compose) …")
	up := exec.CommandContext(ctx, "docker", "compose", "-f", o.composeFile, "up", "-d", "--wait") //nolint:gosec // operator-supplied compose file on the dev CLI
	up.Stdout, up.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := up.Run(); err != nil {
		return fmt.Errorf("docker compose up (is Docker running?): %w", err)
	}
	return nil
}

// devMigrate applies the SQL migrations to the isolated dev database.
func devMigrate(ctx context.Context, cmd *cobra.Command, o devOptions) error {
	devPrintln(cmd.OutOrStdout(), "▸ migrating "+devDBName+" …")
	mig := exec.CommandContext(ctx, "migrate", "-path", o.migrations, "-database", devDatabaseURL, "up") //nolint:gosec // operator-supplied migrations path on the dev CLI
	mig.Stdout, mig.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := mig.Run(); err != nil {
		return fmt.Errorf("applying migrations (is golang-migrate installed?): %w", err)
	}
	return nil
}

// ensureDevDatabase creates the isolated leoflow_dev database if it does not yet
// exist, so the dev experience never shares the product's "leoflow" database.
func ensureDevDatabase(ctx context.Context, cmd *cobra.Command) error {
	conn, err := pgx.Connect(ctx, devMaintenanceURL)
	if err != nil {
		return fmt.Errorf("connecting to Postgres (is it up?): %w", err)
	}
	defer func() { _ = conn.Close(ctx) }() //nolint:errcheck // best-effort close of a short-lived maintenance connection
	var exists bool
	if qerr := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", devDBName).Scan(&exists); qerr != nil {
		return fmt.Errorf("checking for %s: %w", devDBName, qerr)
	}
	if exists {
		return nil
	}
	devPrintln(cmd.OutOrStdout(), "▸ creating isolated dev database "+devDBName+" …")
	//nolint:gosec // G201: the database name is a fixed constant, sanitized as an identifier.
	if _, eerr := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{devDBName}.Sanitize()); eerr != nil {
		return fmt.Errorf("creating database %s: %w", devDBName, eerr)
	}
	return nil
}

// devHome returns the isolated dev state directory (~/.leoflow/dev), created on
// demand. All dev state (the venv, etc.) lives here, never in the project.
func devHome() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	d := filepath.Join(h, ".leoflow", "dev")
	if mkerr := os.MkdirAll(d, 0o750); mkerr != nil {
		return "", fmt.Errorf("creating dev home %s: %w", d, mkerr)
	}
	return d, nil
}

// venvPython returns the Python interpreter inside the dev venv under home,
// honoring the platform layout (bin on Unix, Scripts on Windows).
func venvPython(home string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "venv", "Scripts", "python.exe")
	}
	return filepath.Join(home, "venv", "bin", "python")
}

// venvPipArgs builds the pip-install argv for the dev venv: the task runtime,
// the Airflow Task SDK, and the project's declared dependencies.
func venvPipArgs(runtimeSrc string, deps []string) []string {
	args := make([]string, 0, 6+len(deps))
	args = append(args, "-m", "pip", "install", "-q", runtimeSrc, taskSDKVersion)
	return append(args, deps...)
}

// ensureDevVenv creates the isolated dev venv if absent and installs the task
// runtime + Airflow SDK + project deps into it (skipping the install when the
// runtime is already present, so reruns are fast). It returns the venv's python
// path, which the agent uses to run user code — the host's Python is untouched.
func ensureDevVenv(ctx context.Context, cmd *cobra.Command, home, runtimeSrc string, deps []string) (string, error) {
	py := venvPython(home)
	if _, err := os.Stat(py); err != nil {
		devPrintln(cmd.OutOrStdout(), "▸ creating isolated dev venv …")
		mk := exec.CommandContext(ctx, "python3", "-m", "venv", filepath.Join(home, "venv")) //nolint:gosec // fixed args
		mk.Stdout, mk.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		if e := mk.Run(); e != nil {
			return "", fmt.Errorf("creating dev venv: %w", e)
		}
	}
	check := exec.CommandContext(ctx, py, "-c", "import leoflow_runtime") //nolint:gosec // py is the managed venv interpreter
	if check.Run() != nil {
		devPrintln(cmd.OutOrStdout(), "▸ installing task runtime + Airflow SDK into the dev venv …")
		install := exec.CommandContext(ctx, py, venvPipArgs(runtimeSrc, deps)...) //nolint:gosec // py is the managed venv interpreter
		install.Stdout, install.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		if e := install.Run(); e != nil {
			return "", fmt.Errorf("installing dev venv packages: %w", e)
		}
	}
	return py, nil
}

// resolveDevBinaries locates the leoflow-server and leoflow-agent binaries,
// honoring explicit flags and falling back to PATH then ./bin.
func resolveDevBinaries(o devOptions) (server, agent string, err error) {
	server, err = resolveBinary(o.serverBin, "leoflow-server")
	if err != nil {
		return "", "", err
	}
	agent, err = resolveBinary(o.agentBin, "leoflow-agent")
	if err != nil {
		return "", "", err
	}
	return server, agent, nil
}

// resolveBinary returns explicit when set, otherwise the binary found on PATH,
// otherwise ./bin/<name> if it exists, otherwise an actionable error.
func resolveBinary(explicit, name string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	local := filepath.Join("bin", name)
	if _, err := os.Stat(local); err == nil {
		// Absolute, because the subprocess executor runs the agent with a different
		// working directory; a relative path would not resolve there.
		return filepath.Abs(local)
	}
	return "", fmt.Errorf("%s not found on PATH or ./bin; run `make build` or pass --%s", name, name)
}

// startDevServer launches the control plane with the subprocess executor and the
// DEV instance name, inheriting the developer's environment for the rest.
func startDevServer(ctx context.Context, cmd *cobra.Command, serverBin, agentBin, workDir, venvPython string) (*exec.Cmd, error) {
	devPrintln(cmd.OutOrStdout(), "▸ starting control plane (subprocess executor) …")
	srv := exec.CommandContext(ctx, serverBin) //nolint:gosec // serverBin is operator-resolved on the dev CLI
	srv.Env = append(os.Environ(),
		"LEOFLOW_EXECUTOR_TYPE=subprocess",
		"LEOFLOW_EXECUTOR_AGENT_PATH="+agentBin,
		// The agent runs on the host and dials the control plane back; 127.0.0.1 is
		// dialable, whereas the server's 0.0.0.0 bind address is not.
		"LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR=127.0.0.1:9091",
		// The agent runs the user's dag.py from the project dir, with the isolated
		// venv's Python, and ships logs to a writable dir (the prod default
		// /var/log/leoflow is not writable on a host).
		"LEOFLOW_EXECUTOR_SUBPROCESS_WORKDIR="+workDir,
		"LEOFLOW_PYTHON="+venvPython,
		"LEOFLOW_LOGS_DIR="+filepath.Join(os.TempDir(), "leoflow-dev-logs"),
		"LEOFLOW_UI_INSTANCE_NAME="+devInstanceName,
		"LEOFLOW_DATABASE_URL="+devDatabaseURL,
		"LEOFLOW_REDIS_URL="+devRedisURL,
		"LEOFLOW_AUTH_JWT_SECRET="+devJWTSecret,
		"LEOFLOW_SECRET_KEY="+devSecretKey,
		"LEOFLOW_AUTH_DEV_NO_AUTH=true",
		"LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true",
	)
	srv.Stdout, srv.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("starting control plane: %w", err)
	}
	return srv, nil
}

// waitForReady polls the control plane's /readyz until it is up or the timeout
// (or context) elapses.
func waitForReady(ctx context.Context, baseURL string) error {
	deadline := time.Now().Add(devReadyTimeout)
	for time.Now().Before(deadline) {
		if devReadyOnce(ctx, baseURL) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return errors.New("control plane did not become ready in time")
}

// devReadyOnce reports whether /readyz currently returns 200.
func devReadyOnce(ctx context.Context, baseURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", http.NoBody)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // health poll; close error is irrelevant
	return resp.StatusCode == http.StatusOK
}

// devWatchLoop compiles + registers the DAG, then re-does it on every save until
// the context is canceled (Ctrl-C).
func devWatchLoop(ctx context.Context, cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, o devOptions, token string) error {
	watched := devWatchPaths(dir, cfg)
	if rerr := devCompileAndRegister(ctx, cmd, dir, o, token); rerr != nil {
		devPrintf(cmd.ErrOrStderr(), "✗ %v\n", rerr)
	}
	snap := projectMtimes(watched)
	devPrintf(cmd.OutOrStdout(), "👀 watching %s — edit and save to reload (Ctrl-C to stop)\n", dir)
	ticker := time.NewTicker(devPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			devPrintln(cmd.OutOrStdout(), "\nstopping dev environment …")
			return nil
		case <-ticker.C:
			cur := projectMtimes(watched)
			if !mtimesChanged(snap, cur) {
				continue
			}
			snap = cur
			devPrintf(cmd.OutOrStdout(), "[%s] change detected → reloading …\n", time.Now().Format("15:04:05"))
			if rerr := devCompileAndRegister(ctx, cmd, dir, o, token); rerr != nil {
				devPrintf(cmd.ErrOrStderr(), "✗ %v\n", rerr)
			}
		}
	}
}

// devWatchPaths lists the project files whose edits should trigger a reload.
func devWatchPaths(dir string, cfg *domain.LeoflowConfig) []string {
	return []string{projectConfigPath(dir), dagSourcePath(dir, cfg)}
}

// devCompileAndRegister compiles the project in-memory (parser + overlay +
// guardrails, no image build) and registers the resulting dag.json with the
// running control plane.
func devCompileAndRegister(ctx context.Context, cmd *cobra.Command, dir string, o devOptions, token string) error {
	dagJSON := filepath.Join(os.TempDir(), "leoflow-dev-dag.json")
	// Each save is a fresh ephemeral version, so a hot reload never collides with
	// the previous registration (dag_versions is unique per dag_id + version).
	opts := compileOptions{output: dagJSON, image: o.image, dagVersion: fmt.Sprintf("dev-%d", time.Now().UnixNano())}
	//nolint:contextcheck // runCompile derives its context from cmd; ctx here is used for registration.
	if cerr := runCompile(cmd, dir, opts); cerr != nil {
		return cerr
	}
	data, err := os.ReadFile(dagJSON) //nolint:gosec // path is leoflow-controlled under TempDir
	if err != nil {
		return fmt.Errorf("reading compiled dag.json: %w", err)
	}
	var spec domain.DAGSpec
	if jerr := json.Unmarshal(data, &spec); jerr != nil {
		return fmt.Errorf("parsing compiled dag.json: %w", jerr)
	}
	status, body, perr := pushVersion(ctx, devUIURL, token, spec.DagID, data)
	if perr != nil {
		return perr
	}
	if status >= http.StatusMultipleChoices {
		return fmt.Errorf("control plane returned %d registering %q: %s", status, spec.DagID, body)
	}
	devPrintf(cmd.OutOrStdout(), "✓ registered %q\n", spec.DagID)
	return nil
}
