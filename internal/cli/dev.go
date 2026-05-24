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
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

// devEnv is the fixed local-development environment label and its defaults. The
// subprocess executor runs user code unsandboxed, so `leoflow dev` is dev-only
// and shouts that fact in the banner and the UI navbar (ADR 0023).
const (
	devInstanceName = "Leoflow · DEV"
	devDatabaseURL  = "postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable"
	devRedisURL     = "redis://localhost:6379/0"
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
	cmd.Flags().StringVar(&o.serverBin, "server-bin", "", "leoflow-server binary (default: PATH, then ./bin)")
	cmd.Flags().StringVar(&o.agentBin, "agent-bin", "", "leoflow-agent binary (default: PATH, then ./bin)")
	cmd.Flags().BoolVar(&o.noUp, "no-up", false, "skip docker compose + migrations (deps already running)")
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
		if uerr := devBringUpDeps(ctx, cmd, o); uerr != nil {
			return uerr
		}
	}
	serverBin, agentBin, berr := resolveDevBinaries(o)
	if berr != nil {
		return berr
	}
	server, serr := startDevServer(ctx, cmd, serverBin, agentBin)
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

// devBringUpDeps starts local Postgres + Redis via docker compose and applies
// the SQL migrations, so a single `leoflow dev` is enough to start from nothing.
func devBringUpDeps(ctx context.Context, cmd *cobra.Command, o devOptions) error {
	devPrintln(cmd.OutOrStdout(), "▸ starting dependencies (docker compose) …")
	up := exec.CommandContext(ctx, "docker", "compose", "-f", o.composeFile, "up", "-d", "--wait") //nolint:gosec // operator-supplied compose file on the dev CLI
	up.Stdout, up.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := up.Run(); err != nil {
		return fmt.Errorf("docker compose up (is Docker running?): %w", err)
	}
	devPrintln(cmd.OutOrStdout(), "▸ applying migrations …")
	mig := exec.CommandContext(ctx, "migrate", "-path", o.migrations, "-database", devDatabaseURL, "up") //nolint:gosec // operator-supplied migrations path on the dev CLI
	mig.Stdout, mig.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := mig.Run(); err != nil {
		return fmt.Errorf("applying migrations (is golang-migrate installed?): %w", err)
	}
	return nil
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
		return local, nil
	}
	return "", fmt.Errorf("%s not found on PATH or ./bin; run `make build` or pass --%s", name, name)
}

// startDevServer launches the control plane with the subprocess executor and the
// DEV instance name, inheriting the developer's environment for the rest.
func startDevServer(ctx context.Context, cmd *cobra.Command, serverBin, agentBin string) (*exec.Cmd, error) {
	devPrintln(cmd.OutOrStdout(), "▸ starting control plane (subprocess executor) …")
	srv := exec.CommandContext(ctx, serverBin) //nolint:gosec // serverBin is operator-resolved on the dev CLI
	srv.Env = append(os.Environ(),
		"LEOFLOW_EXECUTOR_TYPE=subprocess",
		"LEOFLOW_EXECUTOR_AGENT_PATH="+agentBin,
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
