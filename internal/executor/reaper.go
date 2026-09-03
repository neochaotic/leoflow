package executor

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// DecisionRecorder records the reaper's per-tick decision metrics. It is the
// narrow slice of the scheduler's metrics sink the reapers need — only the one
// counter — so the executor depends on a capability, not on the observability
// package. A nil DecisionRecorder is tolerated (tests need not stub it).
type DecisionRecorder interface {
	RecordSchedulerDecision(decisionType string)
}

// ReaperStore is the full store surface the four reapers need, composed from
// each reaper's own slice. The metadatabase-backed SchedulerStore satisfies all
// four, so production wires through one type; a unit test fakes just the slice
// its reaper touches.
type ReaperStore interface {
	ReapStore
	HeartbeatReapStore
	DispatchLostReapStore
	PodLostReapStore
	WarmWorkerLostReapStore
}

// Default reaper thresholds. These carry over verbatim from the values the
// scheduler used to own; they are the floor a stuck run/TI/pod must sit idle
// before a backstop reaps it.
//
//   - orphan (5m):        a running dag run may stay quiet this long before the
//     run reaper declares it orphaned — well above any healthy tick or hand-off.
//   - agent-lost (90s):   6x the default 15s agent heartbeat interval, tolerating
//     a handful of missed pings before failing the TI as agent_lost.
//   - dispatch-lost (3m): well above healthy dispatch latency on Lite or Pro, so
//     a live dispatch is never reaped but a stuck queued TI is caught quickly.
//   - pod-lost (60s):     a floor against a transient "pod not listed yet" blip
//     right after the running transition; the reap still needs a no-live-pod read.
const (
	defaultOrphanThreshold       = 5 * time.Minute
	defaultAgentLostThreshold    = 90 * time.Second
	defaultDispatchLostThreshold = 3 * time.Minute
	defaultPodLostGrace          = 60 * time.Second
	// defaultAgentLostGrace is the post-leadership window during which the
	// agent-lost reaper does not fire (#858). Set to 2× the agent-lost threshold
	// so a single control-plane restart + re-election (whose recorded silence can
	// approach the threshold) cannot trip a mass false reap, while staying well
	// under the 10-minute agent token TTL so a re-heartbeat still authenticates.
	// Ladder: heartbeat(15s) < threshold(90s) < grace(180s) < tokenTTL(600s).
	defaultAgentLostGrace = 2 * defaultAgentLostThreshold
	// defaultPodLostLeaderGrace is the post-leadership window during which the
	// pod-lost reaper does not fire. The same control-plane restart that makes
	// heartbeats look stale also makes a task pod that FINISHED during the outage
	// look lost: its container exited, so it is no longer Pending/Running, yet its
	// TI is still `running` because the terminal report never reached a server.
	// The pod's durable outcome record (termination log) is recovered by the
	// reconciler on its own, slower sweep; if the pod-lost reaper fires first it
	// marks a succeeded task failed and the reconciler then finds a settled row.
	// Reusing the agent-lost grace keeps one ladder value for "one restart fault"
	// and leaves the reconciler (30s sweep) several passes to recover the true
	// outcome before this reaper may act. It is additive to PodLostGrace, which
	// is a per-task liveness floor measured from the TI's running transition.
	defaultPodLostLeaderGrace = defaultAgentLostGrace
)

// ReaperConfig holds the idle thresholds and post-leadership graces the reapers
// apply. Zero values are legal but reap aggressively; callers pass
// DefaultReaperConfig unless a test or load harness deliberately overrides them.
type ReaperConfig struct {
	OrphanThreshold       time.Duration
	AgentLostThreshold    time.Duration
	AgentLostGrace        time.Duration
	DispatchLostThreshold time.Duration
	PodLostGrace          time.Duration
	// PodLostLeaderGrace suppresses pod-lost reaping for this long after this
	// instance acquires leadership. See defaultPodLostLeaderGrace.
	PodLostLeaderGrace time.Duration
}

// DefaultReaperConfig returns the production thresholds — the exact values the
// scheduler configured before the reapers moved here.
func DefaultReaperConfig() ReaperConfig {
	return ReaperConfig{
		OrphanThreshold:       defaultOrphanThreshold,
		AgentLostThreshold:    defaultAgentLostThreshold,
		AgentLostGrace:        defaultAgentLostGrace,
		DispatchLostThreshold: defaultDispatchLostThreshold,
		PodLostGrace:          defaultPodLostGrace,
		PodLostLeaderGrace:    defaultPodLostLeaderGrace,
	}
}

// inLeaderGrace reports whether now falls within the post-leadership grace: a
// leaderSince accessor is wired, the grace is positive, leadership is currently
// held (non-zero stamp), and less than grace has elapsed since it was acquired.
// A nil accessor (Lite / no leadership) or a zero grace disables the check.
//
// It is the single definition of the leader-settling gate the reapers share. A
// control-plane restart manufactures the signals several reapers act on (a stale
// heartbeat, a task pod that exited during the outage), so a freshly elected
// leader must let the fleet re-heartbeat and the reconciler recover durable
// outcomes before any of them fires; a genuinely lost task stays stale past the
// grace and is reaped on a later tick. Measured from leadership acquisition, not
// process start, so a re-election also resets it. If leadership flaps with a
// period shorter than the grace the stamp keeps moving and the gated reapers
// never fire — the orphan-run reaper is the backstop for that pathological case.
func inLeaderGrace(leaderSince func() time.Time, grace time.Duration, now time.Time) bool {
	if leaderSince == nil || grace <= 0 {
		return false
	}
	ls := leaderSince()
	return !ls.IsZero() && now.Sub(ls) < grace
}

// destructiveGate reports whether a reaper may take a destructive action — mark
// a TI or run failed, delete a pod — right now. Every reaper consults it
// immediately before each such call (not only at the top of the tick), so a
// shutdown or leader step-down that lands after the candidate list was read
// still stops the write that would follow. A nil gate is open (Lite / tests).
//
// The invariant: a leader that is terminating or stepping down must not mark or
// delete. Its context is being canceled, its view of the fleet is about to go
// stale, and the successor redoes every reap under its own post-leadership
// grace — so a destructive write on the way out is at best redundant and at
// worst the false reap the grace exists to prevent.
type destructiveGate func(ctx context.Context) bool

// gateOpen evaluates a possibly-nil destructiveGate.
func gateOpen(g destructiveGate, ctx context.Context) bool {
	return g == nil || g(ctx)
}

// Reaper is the execution-side backstop that fails stuck runs and task instances
// the scheduler dispatched but that then went dark. It bundles the five
// independent reapers behind one ReapOnce entrypoint the scheduler drives once
// per leader tick, so the scheduler depends on a single capability rather than
// wiring each reaper itself.
type Reaper struct {
	orphan         *orphanReaper
	agentLost      *agentLostReaper
	dispatchLost   *dispatchLostReaper
	podLost        *podLostReaper
	warmWorkerLost *warmWorkerLostReaper
	logger         *slog.Logger
	rec            DecisionRecorder
	// inStepDown reports whether the scheduler is in a graceful leader step-down.
	// It downgrades an expected context-cancel fanout of the run-context to WARN
	// (#311) and, with ctx and leading, closes the destructive gate: a leader
	// stepping down must not mark or delete. Nil is tolerated ("not stepping
	// down").
	inStepDown func() bool
	// leading reports whether this instance currently holds scheduler leadership;
	// part of the destructive gate. Nil is tolerated ("leading") so Lite and
	// tests without an election are unaffected.
	leading func() bool
}

// NewReaper constructs the reapers and wires their pod-teardown / liveness
// capability (pods) and, for the two K8s-aware reapers, the presence cache. The
// wiring mirrors exactly what the scheduler used to do inline: every reaper gets
// pods; only the dispatch-lost and pod-lost reapers get the cache. Nil pods
// (Lite/subprocess) keeps every reaper DB-only and makes the pod-lost reaper a
// no-op, byte-for-byte as before.
//
// warmPods is the live warm-pod seam (ADR 0058 N1d-a2), threaded to the two warm
// consumers exactly the way pods/cache are threaded: the warm-worker-lost reaper
// (which recovers a dead worker's attempts) and the dispatch-lost reaper's H3
// defer. Nil (warm pools off / not wired) makes the warm reaper a no-op and the
// dispatch-lost warm defer inert — with the flag off no TI ever carries a
// warm_worker_id either, so both warm paths are doubly inert, byte-for-byte today.
func NewReaper(store ReaperStore, pods PodManager, cache PodPresenceCache, warmPods WarmPodLister, rec DecisionRecorder, logger *slog.Logger, cfg ReaperConfig, inStepDown func() bool) *Reaper {
	orphan := newOrphanReaper(store, logger, cfg.OrphanThreshold, rec)
	orphan.pods = pods

	agentLost := newAgentLostReaper(store, logger, cfg.AgentLostThreshold, rec)
	agentLost.pods = pods
	agentLost.grace = cfg.AgentLostGrace

	dispatchLost := newDispatchLostReaper(store, logger, cfg.DispatchLostThreshold, rec)
	dispatchLost.pods = pods
	dispatchLost.cache = cache
	dispatchLost.warmPods = warmPods

	podLost := newPodLostReaper(store, logger, cfg.PodLostGrace, rec)
	podLost.pods = pods
	podLost.cache = cache
	podLost.leaderGrace = cfg.PodLostLeaderGrace

	warmWorkerLost := newWarmWorkerLostReaper(store, logger, rec)
	warmWorkerLost.warmPods = warmPods

	r := &Reaper{
		orphan:         orphan,
		agentLost:      agentLost,
		dispatchLost:   dispatchLost,
		podLost:        podLost,
		warmWorkerLost: warmWorkerLost,
		logger:         logger,
		rec:            rec,
		inStepDown:     inStepDown,
	}
	// One destructive gate, shared by every reaper: each re-checks it right before
	// a mark or delete, so a cancel/step-down that lands mid-tick still stops the
	// write. The closure reads r's predicates at call time, so SetLeading may be
	// wired after construction.
	orphan.gate = r.mayReap
	agentLost.gate = r.mayReap
	dispatchLost.gate = r.mayReap
	podLost.gate = r.mayReap
	warmWorkerLost.gate = r.mayReap
	return r
}

// SetLeading wires the leadership predicate into the destructive gate, so a
// reaper tick on an instance that no longer holds leadership marks and deletes
// nothing. The scheduler already drives ReapOnce only while leading; this is the
// defensive re-check at the point of the write. Nil leaves the gate on ctx and
// step-down alone.
func (r *Reaper) SetLeading(fn func() bool) {
	r.leading = fn
}

// mayReap is the destructive gate: open only while the tick's context is live,
// the scheduler is not stepping down, and (when wired) this instance leads.
func (r *Reaper) mayReap(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if r.inStepDown != nil && r.inStepDown() {
		return false
	}
	if r.leading != nil && !r.leading() {
		return false
	}
	return true
}

// SetLogSink wires the sink the agent-lost reaper uses to append a final
// "killed: agent_lost" marker to a reaped attempt's log stream (#861), so a
// killed task's log ends with the reason instead of a silent truncation. Any
// logs.Sink satisfies the parameter; nil (Lite / unwired) leaves markers off.
func (r *Reaper) SetLogSink(s logSink) {
	r.agentLost.sink = s
}

// SetLeaderSince wires the accessor the leadership-gated reapers use to measure
// their post-leadership grace — the window after a (re-)election during which a
// stale heartbeat or a vanished task pod is assumed to be the outage's doing, not
// a dead agent or a lost pod, and is not reaped (see inLeaderGrace). It reaches
// every reaper that acts on a restart-manufactured signal: agent-lost and
// pod-lost. Nil (Lite / no leadership) leaves the grace off. The warm-worker-lost
// reaper is deliberately not gated: its signal is the warm pod's own liveness,
// which a control-plane restart does not disturb.
func (r *Reaper) SetLeaderSince(fn func() time.Time) {
	r.agentLost.leaderSince = fn
	r.podLost.leaderSince = fn
}

// ReapOnce runs the five reapers once, in the same order the scheduler drove
// them: orphan-run, then agent-lost, then dispatch-lost, then pod-lost, then
// warm-worker-lost. The dispatch-lost reaper runs AFTER the orphan-run reaper so
// a clean stuck-queued -> failed -> orphan-run-failed chain can complete in a
// single tick once the thresholds elapse.
//
// The whole tick is skipped — and the skip metered as reap_gate_skip — when the
// destructive gate is closed on entry: a canceled context (SIGTERM drain, the
// step-down cancel fan-out), a scheduler in step-down, or a lost leadership. The
// reapers also re-check the gate before each individual write, so this early
// return is the cheap common case, not the only defense.
//
// Each reaper's infra-level list error is logged and metered but never returned:
// the reapers are independent backstops, so one's failure must not block the
// others, and a list error must not stall the scheduler tick. Per-candidate
// failures are already isolated inside each reaper's run. ReapOnce therefore
// always returns nil today; the error return is kept for the seam so a future
// hard-failure mode need not change the scheduler.
func (r *Reaper) ReapOnce(ctx context.Context) error {
	if !r.mayReap(ctx) {
		r.record("reap_gate_skip")
		return nil
	}
	if err := r.orphan.run(ctx); err != nil {
		r.logError("orphan reaper", err)
		r.record("orphan_list_error")
	}
	if err := r.agentLost.run(ctx); err != nil {
		r.logError("agent-lost reaper", err)
		r.record("agent_lost_list_error")
	}
	if err := r.dispatchLost.run(ctx); err != nil {
		r.logError("dispatch-lost reaper", err)
		r.record("dispatch_lost_list_error")
	}
	if err := r.podLost.run(ctx); err != nil {
		r.logError("pod-lost reaper", err)
		r.record("pod_lost_list_error")
	}
	if err := r.warmWorkerLost.run(ctx); err != nil {
		r.logError("warm-worker-lost reaper", err)
		r.record("warm_worker_lost_list_error")
	}
	return nil
}

// logError mirrors the scheduler's logSchedulerError: downgrade a reaper's error
// to WARN only when it wraps context.Canceled AND the scheduler is currently in a
// graceful leader step-down (the run-context is canceled as the leader releases
// the advisory lock, and every in-flight reaper returns "context canceled"
// milliseconds later — a non-event, #311). Any other error, or a cancel outside a
// known step-down, stays ERROR: the tripwire that catches an unexpected cancel.
func (r *Reaper) logError(msg string, err error) {
	if r.inStepDown != nil && r.inStepDown() && errors.Is(err, context.Canceled) {
		r.logger.Warn(msg+" canceled (leader step-down in progress)", "error", err, "expected", true)
		return
	}
	r.logger.Error(msg, "error", err)
}

func (r *Reaper) record(decision string) {
	if r.rec != nil {
		r.rec.RecordSchedulerDecision(decision)
	}
}
