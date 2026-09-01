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
)

// ReaperConfig holds the four idle thresholds the reapers apply. Zero values are
// legal but reap aggressively; callers pass DefaultReaperConfig unless a test or
// load harness deliberately overrides them.
type ReaperConfig struct {
	OrphanThreshold       time.Duration
	AgentLostThreshold    time.Duration
	AgentLostGrace        time.Duration
	DispatchLostThreshold time.Duration
	PodLostGrace          time.Duration
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
	}
}

// Reaper is the execution-side backstop that fails stuck runs and task instances
// the scheduler dispatched but that then went dark. It bundles the four
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
	// inStepDown reports whether the scheduler is in a graceful leader step-down,
	// so an expected context-cancel fanout of the run-context logs at WARN, not
	// ERROR (#311). Nil is tolerated (treated as "not stepping down").
	inStepDown func() bool
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

	warmWorkerLost := newWarmWorkerLostReaper(store, logger, rec)
	warmWorkerLost.warmPods = warmPods

	return &Reaper{
		orphan:         orphan,
		agentLost:      agentLost,
		dispatchLost:   dispatchLost,
		podLost:        podLost,
		warmWorkerLost: warmWorkerLost,
		logger:         logger,
		rec:            rec,
		inStepDown:     inStepDown,
	}
}

// SetLogSink wires the sink the agent-lost reaper uses to append a final
// "killed: agent_lost" marker to a reaped attempt's log stream (#861), so a
// killed task's log ends with the reason instead of a silent truncation. Any
// logs.Sink satisfies the parameter; nil (Lite / unwired) leaves markers off.
func (r *Reaper) SetLogSink(s logSink) {
	r.agentLost.sink = s
}

// SetLeaderSince wires the accessor the agent-lost reaper uses to measure its
// post-leadership grace (#858) — the window after a (re-)election during which a
// stale heartbeat is assumed to be the outage's doing, not a dead agent, and is
// not reaped. Nil (Lite / no leadership) leaves the grace off.
func (r *Reaper) SetLeaderSince(fn func() time.Time) {
	r.agentLost.leaderSince = fn
}

// ReapOnce runs the four reapers once, in the same order the scheduler drove
// them: orphan-run, then agent-lost, then dispatch-lost, then pod-lost. The
// dispatch-lost reaper runs AFTER the orphan-run reaper so a clean
// stuck-queued -> failed -> orphan-run-failed chain can complete in a single
// tick once the thresholds elapse.
//
// Each reaper's infra-level list error is logged and metered but never returned:
// the reapers are independent backstops, so one's failure must not block the
// others, and a list error must not stall the scheduler tick. Per-candidate
// failures are already isolated inside each reaper's run. ReapOnce therefore
// always returns nil today; the error return is kept for the seam so a future
// hard-failure mode need not change the scheduler.
func (r *Reaper) ReapOnce(ctx context.Context) error {
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
