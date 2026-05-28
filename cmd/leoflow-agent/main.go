// Command leoflow-agent runs as PID 1 inside every task pod. It connects back to
// the control plane over gRPC, fetches the task spec, runs the user process while
// streaming logs, pushes the return value, and reports the terminal state.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/neochaotic/leoflow/internal/agent"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	addr := os.Getenv("LEOFLOW_CONTROL_PLANE_ADDR")
	token := os.Getenv("LEOFLOW_AGENT_TOKEN")
	allowInsecure := os.Getenv("LEOFLOW_AGENT_INSECURE") != "false"
	caFile := os.Getenv("LEOFLOW_AGENT_TLS_CA") // PEM CA to verify the server cert (TLS)

	client, conn, err := agent.Dial(addr, token, allowInsecure, caFile)
	if err != nil {
		slog.Error("connecting to control plane", "error", err)
		return 1
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			slog.Warn("closing connection", "error", cerr)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	sink, err := agent.OpenLogSink(ctx, client)
	if err != nil {
		slog.Warn("log streaming unavailable; logs will not be shipped this run", "error", err)
		sink = agent.NoopLogSink{}
	}

	hostname, herr := os.Hostname()
	if herr != nil {
		hostname = "unknown"
	}

	returnPath, cleanupReturn, rverr := agent.NewReturnValuePath()
	if rverr != nil {
		slog.Error("preparing return-value path", "error", rverr)
		return 1
	}
	defer func() { _ = cleanupReturn() }() //nolint:errcheck // best-effort cleanup of the per-task temp dir on exit

	runner := &agent.Runner{
		Client:            client,
		Cmd:               agent.NewExecRunner(),
		Sink:              sink,
		Hostname:          hostname,
		Version:           version,
		Env:               os.Environ(),
		ReturnPath:        returnPath,
		HeartbeatInterval: 15 * time.Second,
	}
	if rerr := runner.Run(ctx); rerr != nil {
		slog.Error("task failed", "error", rerr)
		return 1
	}
	return 0
}
