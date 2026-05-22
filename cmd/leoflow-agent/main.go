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

	client, conn, err := agent.Dial(addr, token, allowInsecure)
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
		slog.Error("opening log stream", "error", err)
		return 1
	}

	hostname, herr := os.Hostname()
	if herr != nil {
		hostname = "unknown"
	}

	runner := &agent.Runner{
		Client:     client,
		Cmd:        agent.NewExecRunner(),
		Sink:       sink,
		Hostname:   hostname,
		Version:    version,
		Env:        os.Environ(),
		ReturnPath: agent.ReturnValuePath,
	}
	if rerr := runner.Run(ctx); rerr != nil {
		slog.Error("task failed", "error", rerr)
		return 1
	}
	return 0
}
