package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WarmRunner is the client side of the warm-worker transport (ADR 0058 D4): a
// long-lived process that registers once, opens the AwaitAssignment bidi stream,
// and serves MANY task attempts — one at a time — each in a fresh forked child.
//
// Two identities are kept deliberately separate:
//
//   - StreamClient carries the worker's BOOTSTRAP identity. Register and the
//     AwaitAssignment control stream run on it and never adopt an attempt token,
//     so the pod's membership in the pool is stable for the worker's whole life.
//   - WorkClient carries each attempt's PER-ATTEMPT identity. Its per-RPC
//     credential reads AttemptTokens, which the loop swaps to the assignment's
//     attempt_token before running. Because attempts are strictly sequential, no
//     two attempts' RPCs are ever in flight at once, so the swap is race-free; and
//     because the swap only touches AttemptTokens (a different TokenSource / dial
//     from the stream), it never disturbs the already-open bootstrap stream, whose
//     authorization header was sent once at stream open.
//
// In production StreamClient and WorkClient are two dials of the same control
// plane (see cmd/leoflow-agent), one bound to the bootstrap TokenSource and one to
// AttemptTokens. They may be the same client only in tests that don't exercise the
// credential.
type WarmRunner struct {
	StreamClient  agentv1.AgentServiceClient
	WorkClient    agentv1.AgentServiceClient
	AttemptTokens *TokenSource

	// NewSink opens a fresh per-attempt log sink on the WorkClient, so each
	// attempt's logs are shipped under its own attempt_token. Nil (or a returned
	// error) falls back to NoopLogSink — logs are best-effort, never fatal.
	NewSink func(ctx context.Context) (LogSink, error)

	Cmd      CommandRunner
	Hostname string
	Version  string
	Env      []string // base process environment (typically os.Environ())

	// PodName is the worker's OWN Kubernetes pod name, read from LEOFLOW_POD_NAME
	// (injected via the downward API) in main.go. It is sent up in WorkerRegister
	// so the control plane can bind a started attempt to it as the durable
	// warm_worker_id (ADR 0058 N1d-a1). Empty outside Kubernetes (e.g. tests); the
	// binding then simply degrades to per-pod liveness for this worker.
	PodName string

	// ScratchDir is the agent-owned per-attempt scratch root. It is wiped and
	// recreated before every attempt (D4 isolation) and holds the return-value,
	// extra-links, xcom-pushes, and reschedule files the runtime writes.
	ScratchDir string

	// TerminationLogPath and HeartbeatInterval mirror the single-shot Runner's
	// fields and are threaded into every per-attempt Runner.
	TerminationLogPath string
	HeartbeatInterval  time.Duration
}

// Run registers the worker once, opens the assignment stream, and serves
// assignments until the stream ends (server close / drain) or ctx is canceled —
// returning nil in both those cases. It returns a non-nil error only on a
// registration failure, a stream transport failure, or a failure to guarantee a
// clean scratch (isolation is fail-closed). A failed TASK is a normal outcome and
// never ends the loop.
func (w *WarmRunner) Run(ctx context.Context, dagVersionID string) error {
	if _, err := w.StreamClient.Register(ctx, &agentv1.RegisterRequest{
		AgentVersion: w.Version,
		Hostname:     w.Hostname,
	}); err != nil {
		return fmt.Errorf("registering warm worker: %w", err)
	}

	stream, err := w.StreamClient.AwaitAssignment(ctx)
	if err != nil {
		return fmt.Errorf("opening assignment stream: %w", err)
	}
	// The mandatory first WorkerMessage names which pool (dag_version) this worker
	// serves, so the control plane only dispatches matching assignments to it.
	if serr := stream.Send(&agentv1.WorkerMessage{
		Msg: &agentv1.WorkerMessage_Register{
			Register: &agentv1.WorkerRegister{DagVersionId: dagVersionID, PodName: w.PodName},
		},
	}); serr != nil {
		return fmt.Errorf("sending worker register: %w", serr)
	}

	for {
		assignment, rerr := stream.Recv()
		if rerr != nil {
			// A closed / drained stream (EOF), or a parent shutdown (SIGTERM,
			// which gRPC surfaces on the stream as codes.Canceled), both mean
			// "stop serving" — exit cleanly. Anything else is a real transport
			// failure worth surfacing.
			if errors.Is(rerr, io.EOF) || status.Code(rerr) == codes.Canceled {
				return nil
			}
			return fmt.Errorf("receiving assignment: %w", rerr)
		}
		if serr := w.serveAssignment(ctx, stream, assignment); serr != nil {
			// A stream / isolation failure is loop-fatal, EXCEPT a canceled stream
			// (parent shutdown mid-attempt) — that is a clean shutdown, not an error.
			if status.Code(serr) == codes.Canceled {
				return nil
			}
			return serr
		}
	}
}

// serveAssignment claims, runs, and completes a single assignment, then signals
// availability for the next. The returned error is loop-fatal (a stream or
// isolation failure); a failed task is NOT such an error — runOneAttempt already
// reported the terminal FAILED state, so it is logged and the worker keeps serving.
func (w *WarmRunner) serveAssignment(ctx context.Context, stream agentv1.AgentService_AwaitAssignmentClient, a *agentv1.WorkAssignment) error {
	// D4 isolation, part 1: a fresh, empty agent scratch before the attempt runs.
	// This is fail-closed — if we cannot guarantee a clean scratch we tear the
	// worker down rather than risk one attempt observing another's writable state.
	// Done BEFORE the ack on purpose: a scratch failure here means we never ack, so
	// the assignment is reclaimed by lease expiry (the N1b1 path already built),
	// rather than being acked-as-started and then stranded on the worker teardown
	// (which would need the N1d reaper to recover). resetScratch is a fast
	// rm+mkdir, so the pre-ack window stays sub-millisecond — no lease-expiry race.
	if err := w.resetScratch(); err != nil {
		return fmt.Errorf("resetting scratch for assignment %q: %w", a.GetAssignmentId(), err)
	}

	// Claim the lease now that we have committed to running: the control plane
	// holds the attempt for us until this ack and reclaims it if we do not ack
	// within lease_seconds. Sent after the scratch reset so "started" is truthful.
	if err := stream.Send(&agentv1.WorkerMessage{
		Msg: &agentv1.WorkerMessage_Ack{
			Ack: &agentv1.AssignmentAck{AssignmentId: a.GetAssignmentId(), Started: true},
		},
	}); err != nil {
		return fmt.Errorf("acking assignment %q: %w", a.GetAssignmentId(), err)
	}

	// Adopt this attempt's own credential for every per-attempt unary RPC. Only
	// AttemptTokens (WorkClient's source) is touched; the bootstrap stream is
	// unaffected. The attempt's identity (run/task/try) is resolved by GetTaskSpec
	// from this token, exactly as a dedicated pod resolves it today.
	w.AttemptTokens.Set(a.GetAttemptToken())

	// D4 isolation, part 2 (fresh env) is inherent: attemptRunner builds a new
	// Runner whose buildEnv runs from scratch each attempt, so no attempt inherits
	// another attempt's secrets, XCom, or run-context env.
	sink := w.openSink(ctx)
	r := w.attemptRunner(sink)

	// A wedged child is bounded by the task's execution_timeout (honored inside
	// execute via a deadline context), so a normal long task cannot hang the worker
	// forever. A hard per-attempt watchdog INDEPENDENT of execution_timeout — to
	// bound a task that declares no timeout and then wedges — is ADR 0058 H3;
	// TODO(N1d): add that watchdog here so a no-timeout wedge cannot pin the slot.
	if aerr := r.runOneAttempt(ctx); aerr != nil {
		// A failed attempt is a normal, already-reported outcome. Keep serving.
		slog.Warn("warm attempt finished with an error",
			"assignment", a.GetAssignmentId(), "error", aerr)
	}

	// Signal availability so the control plane may dispatch the next assignment.
	if err := stream.Send(&agentv1.WorkerMessage{
		Msg: &agentv1.WorkerMessage_SlotFree{SlotFree: &agentv1.SlotFree{}},
	}); err != nil {
		return fmt.Errorf("signaling slot free after assignment %q: %w", a.GetAssignmentId(), err)
	}
	return nil
}

// resetScratch removes and recreates the agent's per-attempt scratch dir. This is
// the D4 filesystem scrub: it deletes everything the previous attempt (or its user
// process) wrote UNDER the agent's own scratch — the return-value, extra-links,
// xcom-pushes, and reschedule files, plus anything else the runtime staged there —
// so no attempt observes another attempt's writable fs state. It deletes ONLY the
// agent-owned scratch; paths outside it (the task image, mounts, the pod's own
// /tmp) are left untouched.
func (w *WarmRunner) resetScratch() error {
	if err := os.RemoveAll(w.ScratchDir); err != nil {
		return fmt.Errorf("removing scratch %q: %w", w.ScratchDir, err)
	}
	if err := os.MkdirAll(w.ScratchDir, 0o700); err != nil {
		return fmt.Errorf("recreating scratch %q: %w", w.ScratchDir, err)
	}
	return nil
}

// openSink opens a fresh per-attempt log sink, falling back to NoopLogSink when no
// factory is configured or the stream cannot be opened (logs are best-effort).
func (w *WarmRunner) openSink(ctx context.Context) LogSink {
	if w.NewSink == nil {
		return NoopLogSink{}
	}
	sink, err := w.NewSink(ctx)
	if err != nil {
		slog.Warn("opening per-attempt log sink; logs will not ship this attempt", "error", err)
		return NoopLogSink{}
	}
	return sink
}

// attemptRunner builds the single-shot Runner that executes one attempt. Its
// per-task output paths live under the agent-owned scratch (wiped between
// attempts), and its Client is the WorkClient bound to AttemptTokens so every
// per-attempt RPC carries the attempt_token.
func (w *WarmRunner) attemptRunner(sink LogSink) *Runner {
	return &Runner{
		Client:             w.WorkClient,
		Cmd:                w.Cmd,
		Sink:               sink,
		Hostname:           w.Hostname,
		Version:            w.Version,
		Env:                w.Env,
		ReturnPath:         filepath.Join(w.ScratchDir, "return_value.json"),
		LinksPath:          filepath.Join(w.ScratchDir, "extra_links.json"),
		PushesPath:         filepath.Join(w.ScratchDir, "xcom_pushes.json"),
		ReschedulePath:     filepath.Join(w.ScratchDir, "reschedule.txt"),
		TerminationLogPath: w.TerminationLogPath,
		HeartbeatInterval:  w.HeartbeatInterval,
		Token:              w.AttemptTokens,
	}
}
