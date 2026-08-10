// Command leoflow-mcp runs the Leoflow Model Context Protocol server (ADR 0050).
// By default it speaks stdio, for a local agent (Claude Desktop/Code) against a
// Lite control plane; it reaches the control plane only through /api/v2, carrying
// the caller's token. It is optional and never part of leoflow-server.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neochaotic/leoflow/internal/mcp"
	apiclient "github.com/neochaotic/leoflow/pkg/client"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	// Logs go to stderr: stdout is the MCP stdio protocol channel and must carry
	// nothing but protocol frames.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	var server string
	flag.StringVar(&server, "server", envOr("LEOFLOW_SERVER_URL", "http://localhost:8080"),
		"control plane base URL")
	flag.Parse()

	// The caller's JWT is passed through to /api/v2 (ADR 0050 D9); empty is
	// acceptable for a loopback Lite with dev auth.
	token := os.Getenv("LEOFLOW_TOKEN")

	apiClient, err := apiclient.New(server, token)
	if err != nil {
		slog.Error("building control-plane client", "error", err)
		return 1
	}

	srv := mcp.NewServer(apiClient, version)
	slog.Info("leoflow-mcp starting", "server", server, "transport", "stdio", "version", version)
	if err := srv.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		slog.Error("mcp server exited", "error", err)
		return 1
	}
	return 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
