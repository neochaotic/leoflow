package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/setup"
)

// Datastore backends for Lite's Postgres. Docker is the default; managed runs a
// relocatable PostgreSQL under ~/.leoflow with no Docker (Fase 2, behind a flag).
const (
	datastoreDocker  = "docker"
	datastoreManaged = "managed"
)

// managedPGPaths returns the managed Postgres bin dir and data dir, both per-user
// under ~/.leoflow (so root and an unprivileged user never share a cluster).
func managedPGPaths() (binDir, dataDir string, err error) {
	h, herr := os.UserHomeDir()
	if herr != nil {
		return "", "", fmt.Errorf("resolving home dir: %w", herr)
	}
	root := filepath.Join(h, ".leoflow")
	return filepath.Join(root, "postgres", "bin"), filepath.Join(root, "pgdata"), nil
}

// startManagedPostgres downloads the relocatable PostgreSQL if needed, initdb's a
// per-user data dir on first run, and starts it listening on localhost:5432 — the
// same address the Docker datastore used, so dev-DB creation, migrations, and the
// server connect unchanged. Idempotent: an already-running cluster is left as is.
// trust auth is safe here: it binds loopback only on a local single-user machine,
// the same threat model as the Docker datastore's well-known password.
func startManagedPostgres(ctx context.Context, cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	h, herr := os.UserHomeDir()
	if herr != nil {
		return fmt.Errorf("resolving home dir: %w", herr)
	}
	root := filepath.Join(h, ".leoflow")
	binDir, err := setup.EnsurePostgres(ctx, setup.EnsureOpts{
		Home: root, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Libc: detectLibc(),
		Stat: os.Stat,
		Logf: func(format string, a ...any) { devPrintf(out, "  "+format+"\n", a...) },
	})
	if err != nil {
		return fmt.Errorf("installing managed Postgres: %w", err)
	}
	dataDir := filepath.Join(root, "pgdata")

	// Already accepting connections? leave it (idempotent across lite runs).
	if exec.CommandContext(ctx, filepath.Join(binDir, "pg_isready"), "-h", "127.0.0.1", "-p", "5432").Run() == nil { //nolint:gosec // managed binary + fixed args
		devPrintln(out, "▸ managed Postgres already running")
		return nil
	}
	if _, serr := os.Stat(filepath.Join(dataDir, "PG_VERSION")); os.IsNotExist(serr) {
		devPrintln(out, "▸ initializing managed Postgres data dir …")
		id := exec.CommandContext(ctx, filepath.Join(binDir, "initdb"), //nolint:gosec // managed binary + fixed args
			"-D", dataDir, "-U", "leoflow", "-A", "trust", "--encoding=UTF8")
		id.Stdout, id.Stderr = io.Discard, cmd.ErrOrStderr()
		if rerr := id.Run(); rerr != nil {
			return fmt.Errorf("initdb: %w", rerr)
		}
	}
	devPrintln(out, "▸ starting managed Postgres (no Docker) …")
	logFile := filepath.Join(root, "dev", "postgres.log")
	if mkErr := os.MkdirAll(filepath.Dir(logFile), 0o750); mkErr != nil {
		return fmt.Errorf("creating postgres log dir: %w", mkErr)
	}
	start := exec.CommandContext(ctx, filepath.Join(binDir, "pg_ctl"), //nolint:gosec // managed binary + fixed args
		"-D", dataDir, "-l", logFile, "-w", "-t", "30",
		"-o", "-p 5432 -h 127.0.0.1 -k "+dataDir, "start")
	start.Stdout, start.Stderr = io.Discard, cmd.ErrOrStderr()
	if rerr := start.Run(); rerr != nil {
		return fmt.Errorf("starting managed Postgres (see %s): %w", logFile, rerr)
	}
	return nil
}

// stopManagedPostgres stops the managed cluster (best-effort), leaving its data
// dir intact for the next run.
func stopManagedPostgres(cmd *cobra.Command) {
	binDir, dataDir, err := managedPGPaths()
	if err != nil {
		return
	}
	if _, serr := os.Stat(filepath.Join(binDir, "pg_ctl")); serr != nil {
		return
	}
	devPrintln(cmd.OutOrStdout(), "▸ stopping managed Postgres …")
	c := exec.CommandContext(context.Background(), filepath.Join(binDir, "pg_ctl"), "-D", dataDir, "stop", "-m", "fast") //nolint:gosec // managed binary + fixed args
	c.Stdout, c.Stderr = io.Discard, io.Discard
	_ = c.Run() //nolint:errcheck // best-effort stop on shutdown
}

// detectLibc reports the host libc ("glibc"/"musl" on linux, "" on darwin) so the
// matching relocatable PostgreSQL build is selected.
func detectLibc() string {
	return setup.Detect(setup.Probe{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		LookPath: exec.LookPath, Stat: os.Stat, Getwd: os.Getwd,
	}).Libc
}
