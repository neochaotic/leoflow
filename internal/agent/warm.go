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

	// Self-lifecycle bounds (ADR 0058 D9/D10/D6/H3), populated from the warm-pod env
	// in main.go. A warm worker that exits is replaced by the reconciler
	// (RestartPolicy:Never + busy-aware create), so bounding its own life is how a
	// pool stays fresh and scales down. Each bound is disabled when zero/unset — a
	// defensive default; the operator config values are non-zero.
	//
	//   - MaxAttempts: drain after this many completed attempts (D9/D10).
	//   - MaxLifetime: drain once the worker is this old (D9/D10).
	//   - IdleTTL: idle-recycle after this long awaiting the next assignment (D6).
	//   - AttemptWatchdog: hard per-attempt ceiling, INDEPENDENT of the task's
	//     execution_timeout, so a task that declares no timeout and then wedges is
	//     still killed and the slot freed (H3).
	MaxAttempts     int
	MaxLifetime     time.Duration
	IdleTTL         time.Duration
	AttemptWatchdog time.Duration
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

	// D9/D10 accounting: bound the worker by attempts served and wall-clock age.
	// Both are checked only BETWEEN attempts (after SlotFree), so a recycle is always
	// graceful — it never interrupts an in-flight attempt.
	workerStart := time.Now()
	attemptsServed := 0

	for {
		assignment, rerr := w.recvAssignment(ctx, stream)
		if rerr != nil {
			// D6 idle-recycle: no assignment arrived within IdleTTL, so recycle this
			// worker for freshness / scale-down. A closed / drained stream (EOF), or a
			// parent shutdown (SIGTERM, surfaced on the stream as codes.Canceled), also
			// mean "stop serving" — all exit cleanly. Anything else is a real transport
			// failure worth surfacing.
			if errors.Is(rerr, errIdleRecycle) {
				slog.Info("warm worker idle-recycle: no assignment within idle TTL", "idle_ttl", w.IdleTTL)
				return nil
			}
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

		// D9/D10 graceful drain: the attempt above is DONE and its SlotFree is sent, so
		// stopping here awaits no more work and lets the process exit — the reconciler
		// then replaces the pod (RestartPolicy:Never). Checked here, never mid-attempt.
		attemptsServed++
		if w.MaxAttempts > 0 && attemptsServed >= w.MaxAttempts {
			slog.Info("warm worker draining: max attempts reached",
				"attempts_served", attemptsServed, "max_attempts", w.MaxAttempts)
			return nil
		}
		if w.MaxLifetime > 0 && time.Since(workerStart) >= w.MaxLifetime {
			slog.Info("warm worker draining: max lifetime reached",
				"age", time.Since(workerStart), "max_lifetime", w.MaxLifetime)
			return nil
		}
	}
}

// errIdleRecycle is the sentinel recvAssignment returns when no assignment arrives
// within IdleTTL, so Run can distinguish a D6 idle-recycle (clean exit) from a real
// stream error.
var errIdleRecycle = errors.New("warm worker idle-recycle")

// recvAssignment receives the next assignment, enforcing the D6 idle-TTL. With a
// non-positive IdleTTL it is a plain blocking Recv (the timeout disabled). Otherwise
// it runs Recv in a goroutine feeding a buffered channel and races it against the
// idle timer and ctx: if the timer wins it returns errIdleRecycle, and the Recv
// goroutine does NOT leak — the channel is buffered (size 1) so the goroutine's send
// never blocks even with no reader, and the blocked Recv unblocks with an error once
// Run returns and the stream closes.
func (w *WarmRunner) recvAssignment(ctx context.Context, stream agentv1.AgentService_AwaitAssignmentClient) (*agentv1.WorkAssignment, error) {
	if w.IdleTTL <= 0 {
		return stream.Recv()
	}
	type recvResult struct {
		a   *agentv1.WorkAssignment
		err error
	}
	recvCh := make(chan recvResult, 1) // buffered so the goroutine can always send and exit
	go func() {
		a, err := stream.Recv()
		recvCh <- recvResult{a, err}
	}()
	timer := time.NewTimer(w.IdleTTL)
	defer timer.Stop()
	select {
	case r := <-recvCh:
		return r.a, r.err
	case <-timer.C:
		return nil, errIdleRecycle
	case <-ctx.Done():
		return nil, ctx.Err()
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

	// H3 per-attempt watchdog: a hard ceiling on this attempt, INDEPENDENT of the
	// task's execution_timeout (which runOneAttempt still honors inside). A task that
	// declares no execution_timeout and then wedges is killed here — the child exec
	// runs on attemptCtx, so the deadline cancel kills it; the attempt then fails and
	// is reported, SlotFree fires below, and the worker keeps serving. A non-positive
	// watchdog disables the extra ceiling (fall back to just execution_timeout).
	attemptCtx := ctx
	if w.AttemptWatchdog > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, w.AttemptWatchdog)
		defer cancel()
	}
	if aerr := r.runOneAttempt(attemptCtx); aerr != nil {
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
