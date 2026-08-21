// Command leoflow-agent runs as PID 1 inside every task pod. It connects back to
// the control plane over gRPC, fetches the task spec, runs the user process while
// streaming logs, pushes the return value, and reports the terminal state.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/neochaotic/leoflow/internal/agent"
	"github.com/neochaotic/leoflow/internal/version"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

// usage is printed for `--help`. leoflow-agent takes no positional args; it is
// configured via environment and normally launched by the control plane.
const usage = `leoflow-agent — runs as PID 1 inside a task pod: connects to the control plane
over gRPC, runs the task, streams logs, and reports the result.

Configured via environment (LEOFLOW_CONTROL_PLANE_ADDR, LEOFLOW_AGENT_TOKEN, …);
there are no positional arguments. Normally launched by the control plane, not
by hand.

Flags:
  --version   print version and exit
  --help, -h  print this help and exit
`

func main() { os.Exit(run()) }

// bootstrapToken resolves the agent's initial bearer and whether a token exchange
// is required (ADR 0055 Fix #3). Under the exchange transport it reads the
// projected ServiceAccount token from LEOFLOW_AGENT_TOKEN_PATH (the bootstrap
// credential to exchange); otherwise it returns LEOFLOW_AGENT_TOKEN unchanged
// (the env-var default). exchange is true only when both the transport marker and
// the token path are set.
func bootstrapToken() (token string, exchange bool, err error) {
	if os.Getenv("LEOFLOW_AGENT_TOKEN_TRANSPORT") == "exchange" {
		if path := os.Getenv("LEOFLOW_AGENT_TOKEN_PATH"); path != "" {
			projected, rerr := agent.ReadTokenFile(path)
			if rerr != nil {
				return "", true, rerr
			}
			return projected, true, nil
		}
	}
	return os.Getenv("LEOFLOW_AGENT_TOKEN"), false, nil
}

func run() int {
	// Answer `--version`/`--help` before connecting, so an operator can query a
	// deployed agent without a reachable control plane (#593). Without this,
	// `--help` falls through to a Dial that errors on a missing address.
	args := os.Args[1:]
	switch {
	case version.WantsVersion(args):
		fmt.Println(version.Get().String())
		return 0
	case version.WantsHelp(args):
		fmt.Print(usage)
		return 0
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	addr := os.Getenv("LEOFLOW_CONTROL_PLANE_ADDR")
	allowInsecure := os.Getenv("LEOFLOW_AGENT_INSECURE") != "false"
	caFile := os.Getenv("LEOFLOW_AGENT_TLS_CA") // PEM CA to verify the server cert (TLS)

	// Bootstrap credential: under the exchange transport (ADR 0055 Fix #3) the
	// control plane projects a ServiceAccount token at LEOFLOW_AGENT_TOKEN_PATH,
	// which the agent exchanges once for a task-scoped JWT after dialing. The
	// default env-var transport leaves the path unset and uses LEOFLOW_AGENT_TOKEN
	// directly (byte-identical to before).
	token, exchange, berr := bootstrapToken()
	if berr != nil {
		slog.Error("reading projected bootstrap token", "error", berr)
		return 1
	}

	client, conn, tokens, err := agent.Dial(addr, token, allowInsecure, caFile)
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

	// Warm-worker mode (ADR 0058): a long-lived process that registers once with
	// the bootstrap token and serves MANY attempts over the AwaitAssignment stream,
	// each in a fresh forked child. Selected by env; the bootstrap credential comes
	// from the same path as single-shot. Anything else falls through to the
	// byte-identical single-shot path below.
	if os.Getenv("LEOFLOW_WARM_WORKER") == "1" {
		return runWarm(ctx, client, addr, token, allowInsecure, caFile)
	}

	// Exchange the projected bootstrap token for the task-scoped JWT before any
	// other RPC, then swap it into the shared TokenSource so every subsequent call
	// authenticates as the task instance. Fails startup if the exchange is refused.
	if exchange {
		if xerr := agent.ExchangeToken(ctx, client, tokens); xerr != nil {
			slog.Error("exchanging bootstrap token for a task-scoped credential", "error", xerr)
			return 1
		}
	}

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
		Client:     client,
		Cmd:        agent.NewExecRunner(),
		Sink:       sink,
		Hostname:   hostname,
		Version:    version.Get().Version,
		Env:        os.Environ(),
		ReturnPath: returnPath,
		// Operator extra-links share the return value's per-task temp dir (#375).
		LinksPath: filepath.Join(filepath.Dir(returnPath), "extra_links.json"),
		// Custom-keyed XCom pushes, same per-task temp dir (multi-key XCom).
		PushesPath: filepath.Join(filepath.Dir(returnPath), "xcom_pushes.json"),
		// A reschedule-mode sensor's next-poke time, same per-task temp dir (#380).
		ReschedulePath: filepath.Join(filepath.Dir(returnPath), "reschedule.txt"),
		// The durable outcome record's destination (the container termination
		// message), set by the executor's podEnv; empty outside a pod (ADR 0052).
		TerminationLogPath: os.Getenv("LEOFLOW_TERMINATION_LOG_PATH"),
		HeartbeatInterval:  agent.DefaultHeartbeatInterval,
		// The heartbeat loop swaps a renewed bearer into this source (ADR 0055
		// Fix #4); the per-RPC credential reads it on every subsequent call.
		Token: tokens,
	}
	// Fault-injection seam for the durable-outcome chaos E2E (ADR 0052): when a DAG
	// sets LEOFLOW_CHAOS_DIE_BEFORE_REPORT to a task state, the agent writes the
	// outcome record and then exits without delivering the report — simulating a pod
	// killed mid-report (OOM/eviction) with the record already on disk. Never set in
	// production; the reconciler must then recover the outcome from the record.
	if want := os.Getenv("LEOFLOW_CHAOS_DIE_BEFORE_REPORT"); want != "" {
		runner.BeforeReport = func(state agentv1.TaskState) {
			if state.String() == want {
				slog.Warn("chaos: exiting before report to simulate a pod killed mid-report", "state", state.String())
				os.Exit(137)
			}
		}
	}
	if rerr := runner.Run(ctx); rerr != nil {
		slog.Error("task failed", "error", rerr)
		return 1
	}
	return 0
}

// runWarm serves the warm-worker loop (ADR 0058). streamClient carries the
// worker's bootstrap identity (Register + AwaitAssignment). A SECOND dial gives
// the WorkClient its own connection bound to an attempt-scoped TokenSource, which
// the loop swaps to each assignment's attempt_token — so the per-attempt work RPCs
// authenticate as the attempt while the stream keeps the bootstrap identity it was
// opened with. The work dial is seeded with the bootstrap token only to satisfy
// Dial's non-empty check; no work RPC fires before the first attempt_token swap.
func runWarm(ctx context.Context, streamClient agentv1.AgentServiceClient, addr, seedToken string, allowInsecure bool, caFile string) int {
	dagVersionID := os.Getenv("LEOFLOW_DAG_VERSION_ID")
	if dagVersionID == "" {
		slog.Error("warm worker requires LEOFLOW_DAG_VERSION_ID")
		return 1
	}

	workClient, workConn, attemptTokens, err := agent.Dial(addr, seedToken, allowInsecure, caFile)
	if err != nil {
		slog.Error("dialing control plane for per-attempt work", "error", err)
		return 1
	}
	defer func() {
		if cerr := workConn.Close(); cerr != nil {
			slog.Warn("closing work connection", "error", cerr)
		}
	}()

	hostname, herr := os.Hostname()
	if herr != nil {
		hostname = "unknown"
	}

	// The agent-owned scratch root, wiped and recreated before every attempt
	// (D4 isolation). Removed on exit.
	scratchDir, err := os.MkdirTemp("", "leoflow-warm-")
	if err != nil {
		slog.Error("preparing warm scratch dir", "error", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(scratchDir) }() //nolint:errcheck // best-effort cleanup on exit

	w := &agent.WarmRunner{
		StreamClient:  streamClient,
		WorkClient:    workClient,
		AttemptTokens: attemptTokens,
		// A fresh per-attempt log sink on the work client, so each attempt's logs
		// ship under its own attempt_token.
		NewSink: func(ctx context.Context) (agent.LogSink, error) {
			return agent.OpenLogSink(ctx, workClient)
		},
		Cmd:      agent.NewExecRunner(),
		Hostname: hostname,
		Version:  version.Get().Version,
		Env:      os.Environ(),
		// The pod's own name, injected by BuildWarmPod via the downward API. Sent
		// up in WorkerRegister so the control plane binds a started attempt to it
		// as the durable warm_worker_id (ADR 0058 N1d-a1). Empty off-cluster.
		PodName:            os.Getenv("LEOFLOW_POD_NAME"),
		ScratchDir:         scratchDir,
		TerminationLogPath: os.Getenv("LEOFLOW_TERMINATION_LOG_PATH"),
		HeartbeatInterval:  agent.DefaultHeartbeatInterval,
	}
	if rerr := w.Run(ctx, dagVersionID); rerr != nil {
		slog.Error("warm worker failed", "error", rerr)
		return 1
	}
	return 0
}
