package cli

import (
	"bytes"
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
	"strconv"
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
	// taskSDKVersion matches the task image (runtime/Dockerfile); the dev venv
	// installs it so dag.py's `from airflow.sdk import ...` resolves.
	taskSDKVersion = "apache-airflow-task-sdk==1.2.1"
	devJWTSecret   = "dev-insecure-jwt-secret-change-me"
	devSecretKey   = "dev-insecure-secret-key-32bytes!"
	// liteTokenTTLSeconds is the lite session lifetime: 30 days. Lite is a local
	// single-user tool, so the server's 1-hour default just means surprise
	// re-logins mid-session.
	liteTokenTTLSeconds = 30 * 24 * 60 * 60
	// liteLoginRateLimit is the per-minute failed-login cap for Lite — generous,
	// because locking out the single local user is pure friction, not security.
	liteLoginRateLimit = 30
	devAdminUser       = "admin@leoflow.local"
	devPollInterval    = 750 * time.Millisecond
	devReadyTimeout    = 30 * time.Second
	// Dev uses ports distinct from the demo/production defaults (8080/9090/9091)
	// so a `leoflow dev` and a demo control plane can run side by side without
	// colliding. --port overrides the HTTP port; the gRPC and metrics ports derive
	// from it (devGRPCPort/devMetricsPort) so multiple Lite instances can coexist.
	devDefaultPort = 8088
	// The gRPC and metrics ports are offset from the HTTP --port so that distinct
	// --port values yield distinct gRPC/metrics ports (letting two Lite instances
	// run on one host). The offsets preserve the historical defaults: the default
	// HTTP port 8088 maps to gRPC 9099 and metrics 9098.
	devGRPCPortOffset    = 1011
	devMetricsPortOffset = 1010
	// Cluster-mode (default) runs real pod-per-task on a dedicated k3d cluster,
	// fully isolated from any product/demo cluster. Pods dial the host control
	// plane's gRPC; host.docker.internal resolves to the host on Docker Desktop.
	devClusterName = "leoflow-dev"
	devNamespace   = "leoflow"
	devPyVersion   = "3.11"
	devBaseImage   = "leoflow-base:py3.11"
)

// devGRPCPort derives the gRPC port from the HTTP --port; see devGRPCPortOffset.
func devGRPCPort(httpPort int) int { return httpPort + devGRPCPortOffset }

// devMetricsPort derives the metrics port from the HTTP --port.
func devMetricsPort(httpPort int) int { return httpPort + devMetricsPortOffset }

// devGRPCBindAddr is the gRPC listen address for the given HTTP --port.
func devGRPCBindAddr(httpPort int) string { return fmt.Sprintf(":%d", devGRPCPort(httpPort)) }

// devMetricsBindAddr is the metrics listen address for the given HTTP --port.
func devMetricsBindAddr(httpPort int) string { return fmt.Sprintf(":%d", devMetricsPort(httpPort)) }

// devHostGRPCAddr is the address task pods dial back for gRPC (cluster mode),
// derived from the HTTP --port; host.docker.internal resolves to the host.
func devHostGRPCAddr(httpPort int) string {
	return fmt.Sprintf("host.docker.internal:%d", devGRPCPort(httpPort))
}

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
	postgres    string // "auto" (default), "docker", or "managed" (relocatable PG, no Docker)
	// Resolved from ~/.leoflow/config.yaml (written by `leoflow setup`), not flags.
	adminHash  string
	adminEmail string
}

// resolveLiteProject picks the project dir for `leoflow lite`. With an explicit
// path argument it uses that. With no argument it uses the configured workspace
// (the directory `leoflow setup` chose), scaffolding a starter DAG there if it
// has no project yet — so a fresh install runs with a bare `leoflow lite`.
func resolveLiteProject(cmd *cobra.Command, args []string) (string, error) {
	if len(args) == 1 {
		p := args[0]
		// An explicit argument must be an existing project dir. Without this check a
		// typo like `leoflow lite uninstall` was swallowed as a project path and
		// failed later with a cryptic "open uninstall/leoflow.yaml". Fail clearly.
		if _, err := os.Stat(filepath.Join(p, "leoflow.yaml")); err != nil {
			return "", fmt.Errorf("no Leoflow project at %q (no leoflow.yaml).\n"+
				"  - run `leoflow lite` with no argument to use your workspace (%s)\n"+
				"  - run `leoflow init %s` to create a project there\n"+
				"  - for other actions see `leoflow --help` (e.g. `leoflow uninstall`)",
				p, defaultWorkspace(cmd), p)
		}
		return p, nil
	}
	dir := defaultWorkspace(cmd)
	if _, err := os.Stat(filepath.Join(dir, "leoflow.yaml")); errors.Is(err, os.ErrNotExist) {
		dagID, serr := scaffoldProject(dir)
		if serr != nil {
			return "", serr
		}
		devPrintf(cmd.OutOrStdout(), "▸ no DAG yet — scaffolded a starter project %q in %s (edit it in the web editor)\n", dagID, dir)
	}
	return dir, nil
}

// defaultWorkspace returns the workspace from config (set by `leoflow setup`),
// falling back to ~/leoflow.
func defaultWorkspace(cmd *cobra.Command) string {
	if c, err := config.Load(configFilePath(cmd), nil); err == nil && c.Workspace != "" {
		return c.Workspace
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "leoflow")
	}
	return "."
}

// devURL is the dev UI/API base for the given HTTP port. It is always localhost
// because the control plane is reachable on loopback regardless of bind address
// (used for the in-process readiness check and token push).
func devURL(port int) string { return fmt.Sprintf("http://localhost:%d", port) }

// displayURL is the URL to show the user, reflecting the bind host. For a
// wildcard bind it resolves the machine's own LAN IP so the printed URL is
// directly reachable from another machine; if detection fails it falls back to a
// placeholder hint.
func displayURL(host string, port int) string {
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		if ip := machineIP(); ip != "" {
			return fmt.Sprintf("http://%s:%d", ip, port)
		}
		return fmt.Sprintf("http://<this-machine-ip>:%d", port)
	default:
		return fmt.Sprintf("http://%s:%d", host, port)
	}
}

// machineIP returns this machine's primary LAN IPv4. It first asks the OS which
// local address would route to a default gateway (a UDP "connect" sets up the
// route without sending any packet), which picks the real LAN interface over
// docker/virtual bridges; it falls back to the first non-loopback IPv4. Empty
// when there is no usable address.
func machineIP() string {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if c, err := (&net.Dialer{}).DialContext(ctx, "udp", "8.8.8.8:80"); err == nil {
		defer func() { _ = c.Close() }() //nolint:errcheck // no packet sent; close is best-effort
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP != nil && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
			if v4 := n.IP.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return ""
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

// bringUpDependencies starts Lite's datastore and returns a cleanup func the
// caller defers. It resolves the "auto" --postgres for the host first (Docker
// Postgres when Docker is present, else a managed relocatable PG), then: the
// Docker path is a no-op cleanup (its container is left up across runs); the
// managed path returns a stop, since this run owns the cluster.
func bringUpDependencies(ctx context.Context, cmd *cobra.Command, o *devOptions) (func(), error) {
	noop := func() {}
	if o.noUp {
		return noop, nil
	}
	o.postgres = autoDatastore(cmd, o.postgres)
	if o.postgres == datastoreManaged {
		// Managed relocatable Postgres, no Docker at all: Lite is Redis-free (XCom
		// on Postgres, in-process log tailer — ADR 0026), so nothing comes up via
		// docker compose.
		if perr := startManagedPostgres(ctx, cmd); perr != nil {
			return noop, perr
		}
		//nolint:contextcheck // stop runs at shutdown with a fresh context; the run's ctx is already canceled
		return func() { stopManagedPostgres(cmd) }, nil
	}
	// Docker datastore: only Postgres (Lite needs no Redis).
	cf, err := resolveComposeFile(o.composeFile)
	if err != nil {
		return noop, err
	}
	o.composeFile = cf
	return noop, devComposeUp(ctx, cmd, *o, "postgres")
}

// resolveComposeFile returns the docker-compose file Lite uses for its local
// Postgres (Lite is Redis-free — ADR 0026). An explicit --compose wins; else a docker-compose.dev.yaml in
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
			dir, err := resolveLiteProject(cmd, args)
			if err != nil {
				return err
			}
			return runDev(cmd, dir, o)
		},
	}
	cmd.Flags().StringVar(&o.executor, "executor", "auto", "execution mode: 'auto' (default; k3d if Docker is present, else subprocess), 'k8s' (dedicated k3d cluster, real pods), or 'subprocess' (host, fast, unsandboxed)")
	cmd.Flags().IntVar(&o.port, "port", devDefaultPort, "HTTP/UI port (dev default 8088, distinct from the demo's 8080)")
	cmd.Flags().StringVar(&o.host, "host", "127.0.0.1", "address to bind the UI/API to; use 0.0.0.0 to reach it from your internal network/VPN (insecure — see the warning)")
	cmd.Flags().StringVar(&o.image, "image", "leoflow-dev:local", "placeholder image recorded in dag.json (subprocess mode only)")
	cmd.Flags().StringVar(&o.composeFile, "compose", "", "compose file for the local Postgres (default: a managed one under ~/.leoflow, materialized on first run)")
	cmd.Flags().StringVar(&o.runtimeSrc, "runtime-src", "runtime/python", "source of the leoflow_runtime package installed into the dev venv")
	cmd.Flags().StringVar(&o.serverBin, "server-bin", "", "leoflow-server binary (default: PATH, then ./bin)")
	cmd.Flags().StringVar(&o.agentBin, "agent-bin", "", "leoflow-agent binary (default: PATH, then ./bin)")
	cmd.Flags().BoolVar(&o.noUp, "no-up", false, "skip docker compose (Postgres already running); the dev DB + venv are still provisioned")
	cmd.Flags().StringVar(&o.postgres, "postgres", datastoreAuto, "Postgres backend: 'auto' (default; the Docker postgres:16 when Docker is present, else a managed relocatable PG under ~/.leoflow on a Unix socket, no Docker), 'docker', or 'managed' (best on full distros; minimal hosts may lack its system libs)")
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
	// Fail fast on a port conflict (a second Lite instance, or another service on
	// the HTTP/gRPC/metrics ports) with a clear message, before starting Docker.
	if perr := preflightDevPorts(ctx, o.host, o.port); perr != nil {
		return perr
	}
	cleanupDeps, uerr := bringUpDependencies(ctx, cmd, &o)
	if uerr != nil {
		return uerr
	}
	defer cleanupDeps() // stops managed Postgres on exit; no-op for the Docker path
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

	// "auto" (the default) uses k3d when Docker is present, else the unsandboxed
	// subprocess executor so `leoflow lite` still runs without Docker.
	o.executor = autoExecutor(cmd, o.executor)

	// Mode-specific setup: the env the control plane runs with and the per-reload
	// build/register strategy. k8s runs real pods on a dedicated k3d cluster;
	// subprocess runs unsandboxed on the host (fast loop).
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
	venvPy, verr := ensureDevVenv(ctx, cmd, home, resolveRuntimeSrc(o.runtimeSrc, home), cfg.Dependencies)
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

// devComposeUp starts the named local datastore services via docker compose. Lite
// brings up only Postgres (it is Redis-free — ADR 0026); the dev's own database
// lives inside it, isolated by name.
func devComposeUp(ctx context.Context, cmd *cobra.Command, o devOptions, services ...string) error {
	devPrintln(cmd.OutOrStdout(), "▸ starting dependencies (docker compose) …")
	var captured bytes.Buffer
	args := append([]string{"compose", "-f", o.composeFile, "up", "-d", "--wait"}, services...)
	up := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // operator-supplied compose file on the dev CLI
	up.Stdout = cmd.OutOrStdout()
	// Tee stderr so the user still sees compose progress while we inspect it to
	// translate a port-allocation failure into an actionable message.
	up.Stderr = io.MultiWriter(cmd.ErrOrStderr(), &captured)
	if err := up.Run(); err != nil {
		return composeUpError(err, captured.String())
	}
	return nil
}

// composeUpError turns a `docker compose up` failure into an actionable message.
// A port-allocation failure — a foreign Postgres already bound to 5432, the
// common real-world conflict — produces a cryptic raw Docker error, so it is
// translated; any other failure keeps the generic "is Docker running?" hint.
func composeUpError(err error, output string) error {
	low := strings.ToLower(output)
	if strings.Contains(low, "already allocated") || strings.Contains(low, "address already in use") || strings.Contains(low, "port is already") {
		return fmt.Errorf("the Postgres port 5432 is already in use — another Postgres is bound to it. Stop it, run `leoflow lite --postgres managed` (a private, socket-only Postgres), or `leoflow lite --no-up` to point at your own (LEOFLOW_DATABASE_URL): %w", err)
	}
	if strings.Contains(low, "unknown command") || strings.Contains(low, "is not a docker command") || strings.Contains(low, "compose") && strings.Contains(low, "not found") {
		return fmt.Errorf("the Docker Compose v2 plugin is not installed (the `docker compose` subcommand is missing). Install it, or run `leoflow lite --postgres managed` for a Docker-free Postgres: %w", err)
	}
	return fmt.Errorf("docker compose up (is Docker running, with the Compose v2 plugin?): %w", err)
}

// preflightDevPorts checks that the HTTP, gRPC, and metrics ports Lite needs are
// free, failing with a clear message that names the busy port — turning the
// server's deep "bind: address already in use" into actionable advice before
// anything starts. Best-effort: a port freed between the check and the bind still
// surfaces the server's own error. The gRPC/metrics ports derive from the HTTP
// --port, so picking a different --port sidesteps a conflict.
func preflightDevPorts(ctx context.Context, host string, port int) error {
	bindHost := host
	if bindHost == "" || bindHost == "0.0.0.0" {
		bindHost = "127.0.0.1"
	}
	checks := []struct {
		role string
		addr string
		num  int
	}{
		{"the HTTP/UI server", net.JoinHostPort(bindHost, strconv.Itoa(port)), port},
		{"the agent gRPC server", fmt.Sprintf(":%d", devGRPCPort(port)), devGRPCPort(port)},
		{"the metrics endpoint", fmt.Sprintf(":%d", devMetricsPort(port)), devMetricsPort(port)},
	}
	var lc net.ListenConfig
	for _, c := range checks {
		ln, err := lc.Listen(ctx, "tcp", c.addr)
		if err != nil {
			return fmt.Errorf("port %d is already in use (needed for %s); another Leoflow Lite may be running — stop it, or pass --port to pick a free port", c.num, c.role)
		}
		_ = ln.Close() //nolint:errcheck // best-effort probe; closing frees the port for the real bind
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
	m, err := migrate.NewWithSourceInstance("iofs", src, devDSNs().migrate)
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
	conn, err := pgx.Connect(ctx, devDSNs().maintenance)
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

// liteDevDir is the per-user Lite scratch dir (~/.leoflow/dev) for state that must
// never be shared between users: the compiled dag.json and task logs. A global
// /tmp path is owned by whoever ran Lite first and then denies every other user
// (the root-vs-non-root "permission denied" trap). Best-effort: it falls back to a
// temp dir only if the home cannot be resolved.
func liteDevDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".leoflow", "dev")
	}
	return os.TempDir()
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
// devBasePython returns the interpreter used to CREATE the dev venv: the managed
// relocatable CPython 3.11 (installed by `leoflow setup` under ~/.leoflow/python)
// when present, since it bundles venv + ensurepip. It falls back to a python3.11
// / python3 on PATH. Using the managed interpreter avoids needing the system
// python3-venv package, which Debian/Ubuntu split out (the common first-run
// failure: "ensurepip is not available").
func devBasePython(home string) string {
	managed := filepath.Join(filepath.Dir(home), "python", "bin", "python3.11")
	if _, err := os.Stat(managed); err == nil {
		return managed
	}
	for _, name := range []string{"python3.11", "python3"} {
		if p, lerr := exec.LookPath(name); lerr == nil {
			return p
		}
	}
	return "python3"
}

// resolveRuntimeSrc returns the leoflow_runtime package source to pip-install
// into the dev venv. An explicit --runtime-src wins; otherwise the repo path
// (source checkout) is used when present; otherwise the copy `leoflow setup`
// extracted under ~/.leoflow/pysrc — a binary-only install has no repo, so the
// repo-relative "runtime/python" does not exist there.
func resolveRuntimeSrc(flagValue, home string) string {
	if flagValue != "" && flagValue != "runtime/python" {
		return flagValue
	}
	if _, err := os.Stat(filepath.Join("runtime", "python", "pyproject.toml")); err == nil {
		return "runtime/python"
	}
	return filepath.Join(filepath.Dir(home), "pysrc", "runtime", "python")
}

func ensureDevVenv(ctx context.Context, cmd *cobra.Command, home, runtimeSrc string, deps []string) (string, error) {
	py := venvPython(home)
	if _, err := os.Stat(py); err != nil {
		devPrintln(cmd.OutOrStdout(), "▸ creating isolated dev venv …")
		base := devBasePython(home)
		mk := exec.CommandContext(ctx, base, "-m", "venv", filepath.Join(home, "venv")) //nolint:gosec // base is the managed CPython or a resolved python3
		mk.Stdout, mk.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		if e := mk.Run(); e != nil {
			return "", fmt.Errorf("creating dev venv with %s (the managed CPython bundles venv; a system python3 may need its python3-venv package): %w", base, e)
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
	// The base image is built from runtime/Dockerfile with the repo as context;
	// a binary install (curl|sh) has no source tree, so fail clearly and point at
	// the local run mode instead of the cryptic "lstat runtime: no such file".
	if _, err := os.Stat(filepath.Join("runtime", "Dockerfile")); err != nil {
		return fmt.Errorf("cluster run mode needs the Leoflow source tree to build the task base image " +
			"(runtime/Dockerfile), which a binary install does not have.\n" +
			"  Use the 'local' run mode: re-run `leoflow setup` and choose 1 (local), " +
			"or set `lite_executor: subprocess` in ~/.leoflow/config.yaml.\n" +
			"  (Cluster mode works when you run `leoflow lite` from a Leoflow source checkout.)")
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
		"LEOFLOW_SERVER_GRPC_ADDR=" + devGRPCBindAddr(port),
		"LEOFLOW_SERVER_METRICS_ADDR=" + devMetricsBindAddr(port),
		// Lite has no OTLP collector locally; disabling the exporter avoids a noisy
		// "connection refused to :4317" every export interval. Prometheus metrics
		// (scraped, not pushed) stay on.
		"LEOFLOW_OBSERVABILITY_OTEL_ENABLED=false",
		// Per-user, under ~/.leoflow — NOT a shared /tmp path. A global
		// /tmp/leoflow-dev-logs is created by whoever runs Lite first and then
		// rejects every other user with "permission denied" (root vs non-root). The
		// user's own .leoflow dir never collides.
		"LEOFLOW_LOGS_DIR=" + filepath.Join(liteDevDir(), "logs"),
		"LEOFLOW_UI_INSTANCE_NAME=" + devInstanceName,
		"LEOFLOW_UI_EDITION=lite",
		"LEOFLOW_DATABASE_URL=" + devDSNs().database,
		// No LEOFLOW_REDIS_URL: Lite runs Redis-free — XCom on Postgres and an
		// in-process log tailer. The empty Redis URL is the signal the server uses
		// to select the embedded backends (ADR 0026).
		"LEOFLOW_AUTH_JWT_SECRET=" + devJWTSecret,
		// Lite is a local, single-user tool: a 1-hour token (the server default)
		// expires mid-session and silently bounces the user to a re-login they did
		// not ask for. Mint 30-day sessions so signing in is a once-a-month event,
		// not an hourly tax.
		fmt.Sprintf("LEOFLOW_AUTH_JWT_TOKEN_TTL_SECONDS=%d", liteTokenTTLSeconds),
		// A local single-user tool should not lock you out for fat-fingering the
		// password a few times (only failures count, but the production default of
		// 5/min is still tight here). Be generous.
		fmt.Sprintf("LEOFLOW_AUTH_LOGIN_RATE_LIMIT_PER_MINUTE=%d", liteLoginRateLimit),
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
		"LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR=127.0.0.1"+devGRPCBindAddr(port),
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
		"LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="+devHostGRPCAddr(port),
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
	opts.output = filepath.Join(liteDevDir(), "dag.json")
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
	data, err := os.ReadFile(opts.output) //nolint:gosec // path is leoflow-controlled under ~/.leoflow
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
