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
// per-user data dir on first run, and starts it on a Unix socket in that data dir
// (TCP disabled) — so dev-DB creation, migrations, and the server reach it only
// through the per-user socket and never collide with, or connect to, a foreign
// Postgres bound to localhost:5432. Idempotent: an already-running cluster is
// left as is. trust auth is safe here: the socket lives in the user's own
// ~/.leoflow, the same single-user threat model as the Docker datastore.
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

	// Our cluster already accepting connections on its socket? leave it (idempotent
	// across lite runs). Probing the socket dir — not 127.0.0.1 — means a foreign
	// Postgres on 5432 is never mistaken for ours.
	if exec.CommandContext(ctx, filepath.Join(binDir, "pg_isready"), "-h", dataDir, "-p", "5432").Run() == nil { //nolint:gosec // managed binary + fixed args
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
	// listen_addresses= (empty) disables TCP; -k keeps the Unix socket in dataDir
	// (named .s.PGSQL.5432 from -p). No shell here, so an empty -c value is the
	// reliable way to turn TCP off — "-h ''" would pass literal quote characters.
	start := exec.CommandContext(ctx, filepath.Join(binDir, "pg_ctl"), //nolint:gosec // managed binary + fixed args
		"-D", dataDir, "-l", logFile, "-w", "-t", "30",
		"-o", "-p 5432 -k "+dataDir+" -c listen_addresses=", "start")
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
