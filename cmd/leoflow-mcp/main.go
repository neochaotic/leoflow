// Command leoflow-mcp runs the Leoflow Model Context Protocol server (ADR 0050).
// It speaks stdio by default (a local agent — Claude Desktop/Code — against a Lite
// control plane) or Streamable HTTP as an optional Pro service (POST /mcp). Either
// way it reaches the control plane only through /api/v2, carrying the caller's
// token, and is never part of leoflow-server.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neochaotic/leoflow/internal/mcp"
	apiclient "github.com/neochaotic/leoflow/pkg/client"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	// Logs go to stderr: on stdio, stdout is the MCP protocol channel and must
	// carry nothing else.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	var server, transport, listen string
	flag.StringVar(&server, "server", envOr("LEOFLOW_SERVER_URL", "http://localhost:8080"),
		"control plane base URL")
	flag.StringVar(&transport, "transport", envOr("LEOFLOW_MCP_TRANSPORT", "stdio"),
		"transport: stdio | http")
	flag.StringVar(&listen, "listen", envOr("LEOFLOW_MCP_LISTEN", ":9099"),
		"listen address for the http transport")
	flag.Parse()

	if transport != "stdio" && transport != "http" {
		slog.Error("unknown transport (want stdio | http)", "transport", transport)
		return 2
	}
	httpMode := transport == "http"

	// stdio: the process token IS the caller's identity. http: identity is the
	// per-request bearer (ADR 0050 D9), so the base client holds NO ambient token
	// — a bearer-less request is refused, never served with a process credential.
	token := os.Getenv("LEOFLOW_TOKEN")
	if httpMode {
		token = ""
	}
	apiClient, err := apiclient.New(server, token)
	if err != nil {
		slog.Error("building control-plane client", "error", err)
		return 1
	}
	srv := mcp.NewServer(apiClient, server, version, httpMode)

	if httpMode {
		return runHTTP(srv, listen, server)
	}
	slog.Info("leoflow-mcp starting", "server", server, "transport", "stdio", "version", version)
	if err := srv.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		slog.Error("mcp server exited", "error", err)
		return 1
	}
	return 0
}

// runHTTP serves the MCP over Streamable HTTP at POST /mcp. Stateless: no session
// state is kept, so a request is authorized purely by its own bearer and the
// service scales active-active. A stray GET/DELETE returns 405 (spec-compliant in
// stateless mode). Shuts down gracefully on SIGINT/SIGTERM.
func runHTTP(srv *mcpsdk.Server, listen, server string) int {
	handler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return srv },
		&mcpsdk.StreamableHTTPOptions{Stateless: true},
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpSrv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		// Fresh deadline for the drain — ctx is already canceled (that's what woke
		// this goroutine), but WithoutCancel keeps its lineage for contextcheck.
		shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutCtx); err != nil {
			slog.Warn("http server shutdown", "error", err)
		}
	}()

	slog.Info("leoflow-mcp starting", "server", server, "transport", "http", "listen", listen, "version", version)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("http server exited", "error", err)
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
