package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" migrate scheme
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	leoflow "github.com/neochaotic/leoflow"
	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/setup"
	"github.com/neochaotic/leoflow/migrations"
)

// devEnv is the fixed local-development environment label and its defaults. The
// subprocess executor runs user code unsandboxed, so `leoflow dev` is dev-only
// and shouts that fact in the banner and the UI navbar (ADR 0023).
const (
	devInstanceName = "Leoflow Lite"
	// devDatabaseURL targets a DEDICATED database, isolated from the product's
	// "leoflow" db so the dev experience never mixes data with product development
	// (no split brain). devMaintenanceURL is used only to CREATE it on first run.
	devDBName         = "leoflow_dev"
	devDatabaseURL    = "postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable"
	devMaintenanceURL = "postgres://leoflow:leoflow@localhost:5432/postgres?sslmode=disable"
	// devMigrateURL is the same dev database via golang-migrate's pgx5 scheme, used
	// to apply the embedded migrations (no source tree / migrate CLI needed).
	devMigrateURL = "pgx5://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable"
	devRedisURL   = "redis://localhost:6379/0"
	// taskSDKVersion matches the task image (runtime/Dockerfile); the dev venv
	// installs it so dag.py's `from airflow.sdk import ...` resolves.
	taskSDKVersion  = "apache-airflow-task-sdk==1.2.1"
	devJWTSecret    = "dev-insecure-jwt-secret-change-me"
	devSecretKey    = "dev-insecure-secret-key-32bytes!"
	devAdminUser    = "admin@leoflow.local"
	devPollInterval = 750 * time.Millisecond
	devReadyTimeout = 30 * time.Second
	// Dev uses ports distinct from the demo/production defaults (8080/9090/9091)
	// so a `leoflow dev` and a demo control plane can run side by side without
	// colliding. --port overrides the HTTP port; gRPC/metrics are fixed for dev.
	devDefaultPort     = 8088
	devGRPCBindAddr    = ":9099"
	devMetricsBindAddr = ":9098"
	// Cluster-mode (default) runs real pod-per-task on a dedicated k3d cluster,
	// fully isolated from any product/demo cluster. Pods dial the host control
	// plane's gRPC; host.docker.internal resolves to the host on Docker Desktop.
	devClusterName  = "leoflow-dev"
	devNamespace    = "leoflow"
	devPyVersion    = "3.11"
	devBaseImage    = "leoflow-base:py3.11"
	devHostGRPCAddr = "host.docker.internal:9099"
)

const (
	ansiReset = "\x1b[0m"
	ansiLite  = "\x1b[100;97m" // white text on a gray background
)

// devOptions holds the resolved flags for a dev run.
type devOptions struct {
	image       string
	executor    string
	host        string
	port        int
	composeFile string
	runtimeSrc  string
	serverBin   string
	agentBin    string
	noUp        bool
	// Resolved from ~/.leoflow/config.yaml (written by `leoflow setup`), not flags.
	adminHash  string
	adminEmail string
}

// devURL is the dev UI/API base for the given HTTP port. It is always localhost
// because the control plane is reachable on loopback regardless of bind address
// (used for the in-process readiness check and token push).
func devURL(port int) string { return fmt.Sprintf("http://localhost:%d", port) }

// displayURL is the URL to show the user, reflecting the bind host. For a
// wildcard bind we cannot know the reachable address, so we hint to use the
// machine's own IP.
func displayURL(host string, port int) string {
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return fmt.Sprintf("http://<this-machine-ip>:%d", port)
	default:
		return fmt.Sprintf("http://%s:%d", host, port)
	}
}

// isLoopbackHost reports whether host keeps the UI reachable only from the
// machine itself (the safe default).
func isLoopbackHost(host string) bool {
	return host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// warnIfExposed alerts when the user asked to bind beyond loopback: a clear
// security warning with real auth, or a notice that no-auth was forced back to
// loopback (resolveBindHost enforces the latter).
func warnIfExposed(out io.Writer, host, adminHash string) {
	if isLoopbackHost(host) {
		return
	}
	if adminHash == "" {
		devPrintf(out, "  NOTE: --host %s ignored — no admin configured, so Lite stays on loopback "+
			"(an unauthenticated control plane is never exposed). Run `leoflow setup` to enable a login first.\n", host)
		return
	}
	devPrintf(out, "  ⚠ SECURITY: binding to %s exposes Leoflow Lite on your network. Lite uses a short "+
		"admin password — only do this on a trusted internal network or VPN, never the public internet.\n", host)
}

// announceReady prints, once the control plane is up, a prominent block with the
// URL to open, the login, and the watched project path — so they are not lost
// above the provisioning output. When the friendly name leoflow.local resolves it
// is shown too; otherwise a one-line tip explains how to enable it.
func announceReady(out io.Writer, host string, port int, adminEmail, dir string) {
	login := "no-auth (loopback only)"
	if adminEmail != "" {
		login = adminEmail
	}
	project := dir
	if abs, err := filepath.Abs(dir); err == nil {
		project = abs
	}
	devPrintf(out, "\n  ✓ Leoflow Lite is ready\n")
	devPrintf(out, "      open:    %s\n", displayURL(host, port))
	if friendlyResolves() {
		devPrintf(out, "      or:      %s\n", fmt.Sprintf("http://%s:%d", friendlyHost, port))
	}
	devPrintf(out, "      login:   %s\n", login)
	devPrintf(out, "      project: %s\n", project)
	if !friendlyResolves() {
		devPrintf(out, "      tip: for %s, add '127.0.0.1 %s' to /etc/hosts (sudo).\n",
			friendlyHost, friendlyHost)
	}
	devPrintf(out, "\n")
}

// friendlyHost is the convenience hostname Leoflow suggests for the local UI.
const friendlyHost = "leoflow.local"

// friendlyResolves reports whether leoflow.local resolves on this machine, so the
// ready banner only offers it when it actually works (a hosts entry or mDNS).
func friendlyResolves() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, friendlyHost)
	return err == nil && len(addrs) > 0
}

// bringUpDependencies starts Lite's local Postgres + Redis via docker compose
// unless --no-up was given, resolving (and materializing) the compose file first.
func bringUpDependencies(ctx context.Context, cmd *cobra.Command, o *devOptions) error {
	if o.noUp {
		return nil
	}
	cf, err := resolveComposeFile(o.composeFile)
	if err != nil {
		return err
	}
	o.composeFile = cf
	return devComposeUp(ctx, cmd, *o)
}

// resolveComposeFile returns the docker-compose file Lite uses for its local
// Postgres + Redis. An explicit --compose wins; else a docker-compose.dev.yaml in
// the working dir (a source checkout) is used; else the compose embedded in the
// binary is materialized under ~/.leoflow, so a binary-only install runs with
// `leoflow lite` alone.
func resolveComposeFile(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if _, err := os.Stat("docker-compose.dev.yaml"); err == nil {
		return "docker-compose.dev.yaml", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	dir := filepath.Join(home, ".leoflow")
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return "", fmt.Errorf("creating %s: %w", dir, mkErr)
	}
	path := filepath.Join(dir, "docker-compose.yaml")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if wErr := os.WriteFile(path, leoflow.DevCompose(), 0o600); wErr != nil {
			return "", fmt.Errorf("writing managed compose %s: %w", path, wErr)
		}
	}
	return path, nil
}

func newLiteCommand() *cobra.Command {
	var o devOptions
	cmd := &cobra.Command{
		Use:     "lite [path]",
		Aliases: []string{"dev"},
		Short:   "Run Leoflow Lite locally with hot reload.",
		Long: "lite is the Leoflow Lite edition: it brings up local dependencies and runs the " +
			"control plane against an isolated local database, registers the DAG, and hot-reloads " +
			"on every save. The UI is served on a Lite port (default 8088, --port), marked with a " +
			"LITE badge, and behind a login (the admin created by `leoflow setup`).\n\nExecutor " +
			"(--executor): 'subprocess' runs tasks unsandboxed on the host with no image build — " +
			"the fast inner loop, best for local use. 'k8s' runs real pod-per-task on a dedicated, " +
			"isolated k3d mini-cluster (leoflow-dev) — highest fidelity, best for development; it " +
			"rebuilds the DAG image on each change.\n\n('leoflow dev' remains as a deprecated alias.)",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runDev(cmd, dir, o)
		},
	}
	cmd.Flags().StringVar(&o.executor, "executor", "k8s", "execution mode: 'k8s' (dedicated k3d cluster, real pods) or 'subprocess' (host, fast, unsandboxed)")
	cmd.Flags().IntVar(&o.port, "port", devDefaultPort, "HTTP/UI port (dev default 8088, distinct from the demo's 8080)")
	cmd.Flags().StringVar(&o.host, "host", "127.0.0.1", "address to bind the UI/API to; use 0.0.0.0 to reach it from your internal network/VPN (insecure — see the warning)")
	cmd.Flags().StringVar(&o.image, "image", "leoflow-dev:local", "placeholder image recorded in dag.json (subprocess mode only)")
	cmd.Flags().StringVar(&o.composeFile, "compose", "", "compose file for local Postgres + Redis (default: a managed one under ~/.leoflow, materialized on first run)")
	cmd.Flags().StringVar(&o.runtimeSrc, "runtime-src", "runtime/python", "source of the leoflow_runtime package installed into the dev venv")
	cmd.Flags().StringVar(&o.serverBin, "server-bin", "", "leoflow-server binary (default: PATH, then ./bin)")
	cmd.Flags().StringVar(&o.agentBin, "agent-bin", "", "leoflow-agent binary (default: PATH, then ./bin)")
	cmd.Flags().BoolVar(&o.noUp, "no-up", false, "skip docker compose (Postgres/Redis already running); the dev DB + venv are still provisioned")
	cmd.AddCommand(newLiteProvisionCommand())
	cmd.AddCommand(newResetPasswordCommand())
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

// devDagImageRef returns the local image tag built for a DAG in cluster-mode.
func devDagImageRef(dagID string) string {
	return "leoflow-dev-" + dagID + ":dev"
}

// k3dCreateArgs builds the argv to create the dedicated dev cluster.
func k3dCreateArgs(cluster string) []string {
	return []string{"cluster", "create", cluster, "--wait"}
}

// k3dImportArgs builds the argv to import local images into the dev cluster (so
// task pods can use them without a registry).
func k3dImportArgs(cluster string, images ...string) []string {
	args := make([]string, 0, len(images)+3)
	args = append(args, "image", "import")
	args = append(args, images...)
	return append(args, "--cluster", cluster)
}

// devKubeconfigPath returns the isolated kubeconfig file under the dev home; the
// control plane is pointed here so it only ever targets the dev cluster.
func devKubeconfigPath(home string) string {
	return filepath.Join(home, "kubeconfig")
}

// baseImageBuildArgs builds the docker argv for the task base image.
func baseImageBuildArgs() []string {
	return []string{"build", "-f", filepath.Join("runtime", "Dockerfile"),
		"--build-arg", "PYTHON_VERSION=" + devPyVersion, "-t", devBaseImage, "."}
}

// kubectlNamespaceArgs builds the kubectl argv that creates the task-pod
// namespace in the dev cluster.
func kubectlNamespaceArgs(kubeconfig string) []string {
	return []string{"--kubeconfig", kubeconfig, "create", "namespace", devNamespace}
}

// devDockerfile is the Dockerfile generated for a project that does not ship its
// own: it layers the DAG source onto the task base image so the agent can import
// it (matching runtime/Dockerfile's PYTHONPATH convention).
func devDockerfile(baseImage, dagSource string, deps []string) string {
	base := filepath.Base(dagSource)
	df := "FROM " + baseImage + "\n"
	// Install the DAG's declared dependencies before COPY so the (rarely-changing)
	// dependency layer is cached across edits to dag.py.
	if len(deps) > 0 {
		df += "RUN pip install --no-cache-dir " + strings.Join(deps, " ") + "\n"
	}
	df += fmt.Sprintf("COPY %s /home/leoflow/%s\nENV PYTHONPATH=/home/leoflow\n", base, base)
	return df
}

// liteBanner renders a high-visibility Lite-environment banner so a developer
// never mistakes the local loop for production. url is the served UI address.
func liteBanner(uiURL string) string {
	line := fmt.Sprintf(" LEOFLOW LITE — local — %s ", uiURL)
	bar := ""
	for range line {
		bar += "─"
	}
	return fmt.Sprintf("%s╭%s╮%s\n%s│%s│%s\n%s╰%s╯%s",
		ansiLite, bar, ansiReset,
		ansiLite, line, ansiReset,
		ansiLite, bar, ansiReset)
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
	// Apply the executor/port chosen in `leoflow setup` (stored in config) as the
	// defaults, unless overridden on the command line. This honors the wizard's
	// choice while keeping --executor/--port changeable per run.
	applyLiteConfigDefaults(cmd, &o)
	if o.port == 0 {
		o.port = devDefaultPort
	}
	uiURL := devURL(o.port)
	out := cmd.OutOrStdout()
	devPrintln(out, liteBanner(uiURL))

	// The admin login is provisioned by `leoflow setup` (hash-only in config).
	// With it, Lite enforces real auth; without it, fall back to no-auth + warn.
	o.adminHash, o.adminEmail = resolveLiteAdmin(cmd, out)

	ctx, stop := signal.NotifyContext(cmdContext(cmd), os.Interrupt, syscall.SIGTERM)
	defer stop()

	warnIfExposed(out, o.host, o.adminHash)
	if uerr := bringUpDependencies(ctx, cmd, &o); uerr != nil {
		return uerr
	}
	// Provision the isolated dev state: own database + own venv (never the
	// product's database or the system Python).
	if derr := ensureDevDatabase(ctx, cmd); derr != nil {
		return derr
	}
	if merr := devMigrate(cmd); merr != nil {
		return merr
	}
	home, herr := devHome()
	if herr != nil {
		return herr
	}
	serverBin, berr := resolveBinary(o.serverBin, "leoflow-server")
	if berr != nil {
		return berr
	}

	// Mode-specific setup: the env the control plane runs with and the per-reload
	// build/register strategy. Cluster-mode (default) runs real pods on a
	// dedicated k3d cluster; subprocess runs unsandboxed on the host (fast loop).
	var serverEnv []string
	var makeReload func(token string) func() error
	if o.executor == "subprocess" {
		serverEnv, makeReload, err = devSubprocessSetup(ctx, cmd, dir, o, home, cfg)
	} else {
		serverEnv, makeReload, err = devClusterSetup(ctx, cmd, dir, o, home, cfg)
	}
	if err != nil {
		return err
	}

	server, serr := startDevServer(ctx, cmd, serverBin, serverEnv)
	if serr != nil {
		return serr
	}
	defer func() { _ = server.Process.Signal(syscall.SIGTERM) }() //nolint:errcheck // best-effort shutdown of the dev server

	if werr := waitForReady(ctx, uiURL); werr != nil {
		return werr
	}
	announceReady(out, o.host, o.port, o.adminEmail, dir)
	// Mint an admin token in-process signed with the dev JWT secret; the control
	// plane validates it by signature + claims, so no login or seeded user is
	// needed (works against a fresh or pre-existing dev database).
	token, terr := auth.MintUserToken(devJWTSecret, time.Hour, auth.User{
		ID: "leoflow-dev", TenantID: "default", Email: devAdminUser, Roles: []string{"admin"},
	})
	if terr != nil {
		return fmt.Errorf("minting dev token: %w", terr)
	}
	return devWatchLoop(ctx, cmd, dir, cfg, makeReload(token))
}

// devSubprocessSetup provisions the isolated venv and returns the subprocess
// server env plus a reload that compiles + registers (no image build) — the fast
// inner loop, but user code runs unsandboxed on the host.
func devSubprocessSetup(ctx context.Context, cmd *cobra.Command, dir string, o devOptions, home string, cfg *domain.LeoflowConfig) (env []string, makeReload func(string) func() error, err error) {
	agentBin, err := resolveBinary(o.agentBin, "leoflow-agent")
	if err != nil {
		return nil, nil, err
	}
	venvPy, verr := ensureDevVenv(ctx, cmd, home, o.runtimeSrc, cfg.Dependencies)
	if verr != nil {
		return nil, nil, verr
	}
	workDir, aerr := filepath.Abs(dir)
	if aerr != nil {
		return nil, nil, fmt.Errorf("resolving project dir: %w", aerr)
	}
	env = subprocessServerEnv(o.host, o.port, agentBin, workDir, venvPy, o.adminHash, o.adminEmail)
	env = append(env, liteEditorEnv(workDir, filepath.Dir(home))...)
	makeReload = func(token string) func() error {
		base := func() error {
			return devCompileAndRegister(ctx, cmd, dir, compileOptions{image: o.image}, token, nil, devURL(o.port))
		}
		return devReportingReload(ctx, base, devURL(o.port), token, dagSourcePath(dir, cfg))
	}
	return env, makeReload, nil
}

// devClusterSetup ensures the task base image, the dedicated k3d cluster, its
// namespace, and an isolated kubeconfig, then returns the Kubernetes-executor
// server env plus a reload that builds the DAG image, imports it into the
// cluster, and registers — real pod-per-task, fully isolated, at the cost of an
// image build per change.
func devClusterSetup(ctx context.Context, cmd *cobra.Command, dir string, o devOptions, home string, cfg *domain.LeoflowConfig) (env []string, makeReload func(string) func() error, err error) {
	if berr := ensureBaseImage(ctx, cmd); berr != nil {
		return nil, nil, berr
	}
	if cerr := ensureDevCluster(ctx, cmd); cerr != nil {
		return nil, nil, cerr
	}
	kubeconfig := devKubeconfigPath(home)
	if kerr := writeDevKubeconfig(ctx, cmd, kubeconfig); kerr != nil {
		return nil, nil, kerr
	}
	if nerr := ensureDevNamespace(ctx, cmd, kubeconfig); nerr != nil {
		return nil, nil, nerr
	}
	if derr := ensureProjectDockerfile(cmd, dir, cfg); derr != nil {
		return nil, nil, derr
	}
	image := devDagImageRef(cfg.DagID)
	makeReload = func(token string) func() error {
		base := func() error {
			opts := compileOptions{image: image, build: true, builder: "docker", dockerfile: "Dockerfile"}
			return devCompileAndRegister(ctx, cmd, dir, opts, token, func() error {
				return k3dImport(ctx, cmd, image)
			}, devURL(o.port))
		}
		return devReportingReload(ctx, base, devURL(o.port), token, dagSourcePath(dir, cfg))
	}
	env = clusterServerEnv(o.host, o.port, kubeconfig, o.adminHash, o.adminEmail)
	if wd, aerr := filepath.Abs(dir); aerr == nil {
		env = append(env, liteEditorEnv(wd, filepath.Dir(home))...)
	}
	return env, makeReload, nil
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

// devMigrate applies the embedded SQL migrations to the isolated dev database.
// The migrations are compiled into the binary (no source tree or migrate CLI),
// a step toward a binaries-only dev install (#60).
func devMigrate(cmd *cobra.Command) error {
	devPrintln(cmd.OutOrStdout(), "▸ migrating "+devDBName+" (embedded) …")
	src, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("loading embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, devMigrateURL)
	if err != nil {
		return fmt.Errorf("initializing migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }() //nolint:errcheck // best-effort close of source + db handles
	if uerr := m.Up(); uerr != nil && !errors.Is(uerr, migrate.ErrNoChange) {
		return fmt.Errorf("applying migrations: %w", uerr)
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

// devRun and devOutput run external dev tools (k3d/docker/kubectl). They are
// package variables so tests can stub the external-tool calls; devRun streams to
// the command's output, devOutput captures combined output for inspection.
var (
	devRun = func(ctx context.Context, cmd *cobra.Command, name string, args ...string) error {
		c := exec.CommandContext(ctx, name, args...) //nolint:gosec // dev tool invoking fixed external binaries
		c.Stdout, c.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		return c.Run()
	}
	devOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // dev tool invoking fixed external binaries
	}
)

// ensureBaseImage builds the task base image (runtime/Dockerfile) if it is not
// already present, so DAG images can layer onto it. Requires the leoflow source
// tree (it builds from runtime/Dockerfile with the repo as context).
func ensureBaseImage(ctx context.Context, cmd *cobra.Command) error {
	if _, err := devOutput(ctx, "docker", "image", "inspect", devBaseImage); err == nil {
		return nil
	}
	devPrintln(cmd.OutOrStdout(), "▸ building task base image "+devBaseImage+" (first run) …")
	if err := devRun(ctx, cmd, "docker", baseImageBuildArgs()...); err != nil {
		return fmt.Errorf("building base image (run from the leoflow source tree): %w", err)
	}
	return nil
}

// ensureDevCluster creates the dedicated k3d cluster if it does not exist.
func ensureDevCluster(ctx context.Context, cmd *cobra.Command) error {
	out, _ := devOutput(ctx, "k3d", "cluster", "list", "--no-headers") //nolint:errcheck // absence is handled below
	if strings.Contains(string(out), devClusterName) {
		return nil
	}
	devPrintln(cmd.OutOrStdout(), "▸ creating dedicated dev cluster "+devClusterName+" (first run) …")
	if err := devRun(ctx, cmd, "k3d", k3dCreateArgs(devClusterName)...); err != nil {
		return fmt.Errorf("creating k3d cluster %s (is k3d installed?): %w", devClusterName, err)
	}
	return nil
}

// writeDevKubeconfig writes the dev cluster's kubeconfig to an isolated file so
// the control plane only ever targets leoflow-dev, never the product cluster.
func writeDevKubeconfig(ctx context.Context, _ *cobra.Command, path string) error {
	out, err := devOutput(ctx, "k3d", "kubeconfig", "get", devClusterName)
	if err != nil {
		return fmt.Errorf("getting kubeconfig for %s: %w", devClusterName, err)
	}
	if werr := os.WriteFile(path, out, 0o600); werr != nil {
		return fmt.Errorf("writing kubeconfig %s: %w", path, werr)
	}
	return nil
}

// ensureDevNamespace creates the task-pod namespace in the dev cluster (idempotent).
func ensureDevNamespace(ctx context.Context, _ *cobra.Command, kubeconfig string) error {
	out, err := devOutput(ctx, "kubectl", kubectlNamespaceArgs(kubeconfig)...)
	if err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("creating namespace %s: %s: %w", devNamespace, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// k3dImport imports a locally-built image into the dev cluster so task pods can
// use it without a registry.
func k3dImport(ctx context.Context, cmd *cobra.Command, image string) error {
	devPrintln(cmd.OutOrStdout(), "▸ importing "+image+" into "+devClusterName+" …")
	if err := devRun(ctx, cmd, "k3d", k3dImportArgs(devClusterName, image)...); err != nil {
		return fmt.Errorf("importing %s into %s: %w", image, devClusterName, err)
	}
	return nil
}

// ensureProjectDockerfile generates a default Dockerfile when the project lacks
// one, layering the DAG source onto the task base image.
func ensureProjectDockerfile(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig) error {
	df := filepath.Join(dir, "Dockerfile")
	if _, err := os.Stat(df); err == nil {
		return nil
	}
	src := cfg.DagSource
	if src == "" {
		src = "dag.py"
	}
	devPrintln(cmd.OutOrStdout(), "▸ generating a default Dockerfile (none found) …")
	if werr := os.WriteFile(df, []byte(devDockerfile(devBaseImage, src, cfg.Dependencies)), 0o600); werr != nil {
		return fmt.Errorf("writing Dockerfile: %w", werr)
	}
	return nil
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

// sharedServerEnv is the Lite control plane environment common to both executor
// modes: the isolated local database, the LITE edition marker, and a writable
// logs dir. When an admin hash is configured (by `leoflow setup`), Lite enforces
// real auth and bootstraps that admin; otherwise it falls back to the dev no-auth
// bypass.
//
// host is the bind address. It is honored only with real auth; a no-auth
// fallback is ALWAYS forced to loopback so an unauthenticated control plane can
// never be exposed to the network (resolveBindHost enforces this).
func sharedServerEnv(host string, port int, adminHash, adminEmail string) []string {
	env := []string{
		fmt.Sprintf("LEOFLOW_SERVER_HTTP_ADDR=%s:%d", resolveBindHost(host, adminHash), port),
		"LEOFLOW_SERVER_GRPC_ADDR=" + devGRPCBindAddr,
		"LEOFLOW_SERVER_METRICS_ADDR=" + devMetricsBindAddr,
		"LEOFLOW_LOGS_DIR=" + filepath.Join(os.TempDir(), "leoflow-dev-logs"),
		"LEOFLOW_UI_INSTANCE_NAME=" + devInstanceName,
		"LEOFLOW_UI_EDITION=lite",
		"LEOFLOW_DATABASE_URL=" + devDatabaseURL,
		"LEOFLOW_REDIS_URL=" + devRedisURL,
		"LEOFLOW_AUTH_JWT_SECRET=" + devJWTSecret,
		"LEOFLOW_SECRET_KEY=" + devSecretKey,
		"LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true",
	}
	if adminHash != "" {
		// Real auth: bootstrap the admin from the hash; no bypass.
		return append(env,
			"LEOFLOW_BOOTSTRAP_PASSWORD_HASH="+adminHash,
			"LEOFLOW_BOOTSTRAP_EMAIL="+adminEmail,
		)
	}
	// No admin configured: dev no-auth fallback (runDev warns; loopback-bound).
	return append(env, "LEOFLOW_AUTH_DEV_NO_AUTH=true")
}

// resolveBindHost returns the address the control plane binds to. A non-loopback
// host (e.g. 0.0.0.0 for internal-network access) is honored only when real auth
// is configured; without an admin (no-auth fallback) it is forced to loopback,
// so an unauthenticated control plane is never exposed beyond the machine.
func resolveBindHost(host, adminHash string) string {
	if host == "" {
		host = "127.0.0.1"
	}
	if adminHash == "" {
		return "127.0.0.1"
	}
	return host
}

// liteEditorEnv enables the Lite web editor (ADR 0025) for the launched server:
// the workspace it edits (the watched project dir) and the directory holding the
// Monaco bundle that `leoflow setup` fetched. Both executors get it — the editor
// is orthogonal to execution.
func liteEditorEnv(workspaceDir, leoflowRoot string) []string {
	return []string{
		"LEOFLOW_UI_WORKSPACE=" + workspaceDir,
		"LEOFLOW_UI_MONACO_DIR=" + setup.MonacoDir(leoflowRoot),
	}
}

// applyLiteConfigDefaults loads ~/.leoflow/config.yaml and applies the recorded
// executor/port to o, unless those flags were set on the command line.
func applyLiteConfigDefaults(cmd *cobra.Command, o *devOptions) {
	c, err := config.Load(configFilePath(cmd), nil)
	if err != nil {
		return
	}
	mergeLiteDefaults(o, c, cmd.Flags().Changed("executor"), cmd.Flags().Changed("port"))
}

// mergeLiteDefaults applies the executor/port from config (written by
// `leoflow setup`) when the corresponding flag was not set on the command line.
func mergeLiteDefaults(o *devOptions, c *config.Config, executorSet, portSet bool) {
	if c == nil {
		return
	}
	if !executorSet && c.LiteExecutor != "" {
		o.executor = c.LiteExecutor
	}
	if !portSet && c.LitePort != 0 {
		o.port = c.LitePort
	}
}

// resolveLiteAdmin loads the configured admin credential and warns when none is
// set (Lite then falls back to no-auth).
func resolveLiteAdmin(cmd *cobra.Command, out io.Writer) (hash, email string) {
	hash, email = loadLiteAdmin(cmd)
	if hash == "" {
		devPrintln(out, "  WARNING: no admin configured — run `leoflow setup`. Falling back to no-auth (local only, insecure).")
	}
	return hash, email
}

// loadLiteAdmin reads the admin credential the setup wizard persisted (hash only)
// from ~/.leoflow/config.yaml. Returns an empty hash when none is configured.
func loadLiteAdmin(cmd *cobra.Command) (hash, email string) {
	c, err := config.Load(configFilePath(cmd), nil)
	if err != nil || c == nil {
		return "", ""
	}
	email = c.AdminEmail
	if email == "" {
		email = "admin@leoflow.local"
	}
	return c.AdminPasswordHash, email
}

// subprocessServerEnv adds the subprocess-executor settings: the agent binary,
// the project workdir (so dag.py imports), the venv Python, and a dialable
// control-plane address (the server binds 0.0.0.0, which is not a dial target).
func subprocessServerEnv(host string, port int, agentBin, workDir, venvPython, adminHash, adminEmail string) []string {
	return append(sharedServerEnv(host, port, adminHash, adminEmail),
		"LEOFLOW_EXECUTOR_TYPE=subprocess",
		"LEOFLOW_EXECUTOR_AGENT_PATH="+agentBin,
		"LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR=127.0.0.1"+devGRPCBindAddr,
		"LEOFLOW_EXECUTOR_SUBPROCESS_WORKDIR="+workDir,
		"LEOFLOW_PYTHON="+venvPython,
	)
}

// clusterServerEnv adds the Kubernetes-executor settings: the isolated dev
// cluster's kubeconfig (so the control plane targets leoflow-dev, never the
// product cluster) and the host address task pods dial back for gRPC.
func clusterServerEnv(host string, port int, kubeconfig, adminHash, adminEmail string) []string {
	return append(sharedServerEnv(host, port, adminHash, adminEmail),
		"LEOFLOW_EXECUTOR_TYPE=kubernetes",
		"KUBECONFIG="+kubeconfig,
		"LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="+devHostGRPCAddr,
		// The dev k3d cluster's local-path provisioner rejects RWX; it is
		// single-node, so RWO is sufficient for a run's sequential pods (ADR 0022).
		"LEOFLOW_EXECUTOR_DEFAULTS_STAGING_ACCESS_MODE=ReadWriteOnce",
	)
}

// startDevServer launches the control plane with the given environment and
// returns once it has started.
func startDevServer(ctx context.Context, cmd *cobra.Command, serverBin string, env []string) (*exec.Cmd, error) {
	devPrintln(cmd.OutOrStdout(), "▸ starting control plane …")
	srv := exec.CommandContext(ctx, serverBin) //nolint:gosec // serverBin is operator-resolved on the dev CLI
	srv.Env = append(os.Environ(), env...)
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

// devWatchLoop runs reload once, then again on every save of a watched file
// until the context is canceled (Ctrl-C). reload encapsulates the mode-specific
// build/register step.
func devWatchLoop(ctx context.Context, cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, reload func() error) error {
	watched := devWatchPaths(dir, cfg)
	if rerr := reload(); rerr != nil {
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
			if rerr := reload(); rerr != nil {
				devPrintf(cmd.ErrOrStderr(), "✗ %v\n", rerr)
			}
		}
	}
}

// devWatchPaths lists the project files whose edits should trigger a reload.
func devWatchPaths(dir string, cfg *domain.LeoflowConfig) []string {
	return []string{projectConfigPath(dir), dagSourcePath(dir, cfg)}
}

// devCompileAndRegister compiles the project (parser + overlay + guardrails),
// optionally runs afterCompile (e.g. import the built image into the cluster),
// and registers the resulting dag.json with the running control plane. Each call
// stamps a fresh dev version so a hot reload never collides with the previous
// registration (dag_versions is unique per dag_id + version).
func devCompileAndRegister(ctx context.Context, cmd *cobra.Command, dir string, opts compileOptions, token string, afterCompile func() error, uiURL string) error {
	opts.output = filepath.Join(os.TempDir(), "leoflow-dev-dag.json")
	opts.dagVersion = fmt.Sprintf("dev-%d", time.Now().UnixNano())
	//nolint:contextcheck // runCompile derives its context from cmd; ctx here is used for registration.
	if cerr := runCompile(cmd, dir, opts); cerr != nil {
		return cerr
	}
	if afterCompile != nil {
		if aerr := afterCompile(); aerr != nil {
			return aerr
		}
	}
	data, err := os.ReadFile(opts.output) //nolint:gosec // path is leoflow-controlled under TempDir
	if err != nil {
		return fmt.Errorf("reading compiled dag.json: %w", err)
	}
	var spec domain.DAGSpec
	if jerr := json.Unmarshal(data, &spec); jerr != nil {
		return fmt.Errorf("parsing compiled dag.json: %w", jerr)
	}
	status, body, perr := pushVersion(ctx, uiURL, token, spec.DagID, data)
	if perr != nil {
		return perr
	}
	if status >= http.StatusMultipleChoices {
		return fmt.Errorf("control plane returned %d registering %q: %s", status, spec.DagID, body)
	}
	devPrintf(cmd.OutOrStdout(), "✓ registered %q\n", spec.DagID)
	return nil
}

// devReportingReload wraps a reload so a failed compile is published to the
// control plane as an import error — lighting the Airflow home's native "Import
// Errors" banner so the failure is visible in the UI, not only the terminal — and
// a good compile clears it. Reporting is best-effort and never masks the reload's
// own result.
func devReportingReload(ctx context.Context, reload func() error, serverURL, token, filename string) func() error {
	return func() error {
		if err := reload(); err != nil {
			_ = reportImportError(ctx, serverURL, token, filename, err.Error()) //nolint:errcheck // best-effort UI hint; the reload error below is authoritative
			return err
		}
		_ = clearImportError(ctx, serverURL, token, filename) //nolint:errcheck // best-effort: clears the banner on a good compile
		return nil
	}
}

// reportImportError records a failed compile as an Airflow import error so the
// UI home banner surfaces it (keyed by filename; replaces any previous error).
func reportImportError(ctx context.Context, serverURL, token, filename, stack string) error {
	body, err := json.Marshal(map[string]string{"filename": filename, "stack_trace": stack, "bundle_name": "leoflow"})
	if err != nil {
		return fmt.Errorf("encoding import error: %w", err)
	}
	return devImportErrorRequest(ctx, http.MethodPut, strings.TrimRight(serverURL, "/")+"/api/v2/importErrors", token, body)
}

// clearImportError removes any recorded import error for a file (a good re-import).
func clearImportError(ctx context.Context, serverURL, token, filename string) error {
	u := strings.TrimRight(serverURL, "/") + "/api/v2/importErrors?filename=" + url.QueryEscape(filename)
	return devImportErrorRequest(ctx, http.MethodDelete, u, token, nil)
}

// devImportErrorRequest issues an authenticated import-error write to the control plane.
func devImportErrorRequest(ctx context.Context, method, reqURL, token string, body []byte) error {
	var r io.Reader
	if body != nil {
		r = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, r)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("calling %s: %w", reqURL, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // response body discarded
	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("import error endpoint returned %d", resp.StatusCode)
	}
	return nil
}
