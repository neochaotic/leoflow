package executor

import (
	"context"
	"log/slog"
)

// WarmTarget is one active dag_version the warm-pool reconciler keeps warm workers
// ready for (model A2, ADR 0058 N1d-b). Image is the DAG's image the warm worker
// runs the agent from.
//
// EffectiveMinIdle is the number of IDLE workers to keep ready for this version —
// the warm buffer, already resolved through the clamp/fallback/flag gate (see
// config.ExecutionSection.EffectiveMinIdle). It is NOT a total pool size: busy
// workers do not count against it, so under load the pool grows past min_idle to
// keep the idle buffer available.
//
// MaxPoolSize is the TOTAL ceiling of live workers (idle + busy) the version may
// hold at once (config.ExecutionSection.MaxPoolSize). The reconciler never creates
// past it, so the pool breathes between EffectiveMinIdle-idle + busy and this cap.
// The N1a boot validation guarantees max_pool_size >= 1 when warm pools are on and
// EffectiveMinIdle is clamped to it upstream, so MaxPoolSize >= EffectiveMinIdle in
// practice; the reconciler still handles MaxPoolSize < EffectiveMinIdle defensively
// by taking the effective ceiling as max(EffectiveMinIdle, MaxPoolSize) — the idle
// buffer must always be creatable.
type WarmTarget struct {
	DagVersionID     string
	Image            string
	EffectiveMinIdle int
	MaxPoolSize      int
}

// WarmTargetSource yields the currently active dag_versions and their effective
// warm target. It is implemented on the storage/scheduler side (it reads active
// runs and their cached specs and applies the operator's clamp/fallback), and is
// defined HERE so the executor's reconciler depends on a narrow capability rather
// than importing the scheduler or storage package (which would be a dependency
// cycle — storage already imports executor).
type WarmTargetSource interface {
	ActiveWarmTargets(ctx context.Context) ([]WarmTarget, error)
}

// BusyWarmWorkerSource yields the set of warm-worker pod names currently serving a
// `running` attempt (ADR 0058 N1d-b): a warm worker is BUSY iff some `running`
// task_instance is durably bound to it (warm_worker_id = the pod's own name — the
// binding landed in N1d-a1/a2). Returned as a set keyed by pod name so the
// reconciler classifies each live pod in O(1).
//
// Implemented on the storage side and defined HERE so the reconciler depends on a
// narrow capability rather than importing storage. With warm pools off no TI is
// ever bound, so the set is always empty and every worker classifies as idle —
// byte-for-byte today's dedicated pod-per-task behavior.
type BusyWarmWorkerSource interface {
	ListBusyWarmWorkerPods(ctx context.Context) (map[string]bool, error)
}

// WarmPodInfo identifies one existing warm-worker pod and the dag_version it
// serves (read from its labels). The reconciler counts these per version.
//
// Terminal marks a warm pod that has reached a terminal phase (Succeeded or
// Failed). Warm pods are RestartPolicy:Never and the agent has no reconnect, so
// a crashed/drained/finished worker lingers as a terminal pod that can never
// serve again. The reconciler must NOT count terminal pods toward the target
// (or a dead worker never gets replaced) and always reaps them.
type WarmPodInfo struct {
	Name         string
	DagVersionID string
	Terminal     bool
}

// WarmPodClient is the cluster side of warm-pool reconciliation: list the warm
// fleet, create a new warm worker for a target (which mints the bootstrap token,
// builds the pod via BuildWarmPod, and Creates it — the auth/config-aware half,
// wired in main.go), and delete one by name. Kept as a narrow seam so the
// reconciler is unit-tested with a fake and the executor imports neither auth nor
// config. KubernetesWarmPods is the production implementation.
type WarmPodClient interface {
	ListWarmPods(ctx context.Context) ([]WarmPodInfo, error)
	CreateWarmPod(ctx context.Context, t WarmTarget) error
	DeleteWarmPod(ctx context.Context, name string) error
}

// WarmPoolReconciler maintains the IDLE warm-worker buffer per active dag_version
// (ADR 0058 N1b2b + N1d-b, model A2). Each tick it reads the active targets, the
// existing warm fleet, and the busy set (pods serving a running attempt), then per
// version partitions the live pods into BUSY and IDLE and:
//   - CREATEs enough workers to restore EffectiveMinIdle IDLE workers, capped by
//     MaxPoolSize (the total ceiling) — so the pool grows past min_idle under load
//     and never exceeds the cap;
//   - DELETEs only EXCESS IDLE workers (idle over the target), never a busy one, so
//     a scale-down or a drain never kills an in-flight attempt (review M1);
//   - reaps terminal pods unconditionally (H1);
//   - drains a no-longer-active version (target 0) down to its idle workers +
//     terminal pods, LEAVING busy workers to finish (they are deleted a later tick
//     once idle).
//
// It is leader-gated (run on a gated ticker, like the pod reconciler) so at
// replicaCount>1 only the leader mutates the fleet. Idempotent (it reconciles to
// the idle target, so re-running converges), panic-safe and per-version isolated
// (one bad dag_version never blocks the others), and O(active versions) per tick.
// It is only constructed and started when warm pools are enabled; with them off
// the reconciler never runs, every warm pool stays empty, and dispatch is
// byte-for-byte today's dedicated pod-per-task.
//
// The busy set is a hard safety input: without it every worker looks idle and a
// busy worker could be deleted, so a nil busy source or a busy-list error aborts
// the tick with zero mutations (do-no-harm).
//
// Deferred beyond this brick: the idle-TTL freshness recycle (D6), the D9 worker
// lifetime / attempts-per-worker caps, and the GC-anchor ConfigMap (D11); a bare
// warm pod deleted here is fine until then.
type WarmPoolReconciler struct {
	targets WarmTargetSource
	pods    WarmPodClient
	busy    BusyWarmWorkerSource
	logger  *slog.Logger
	rec     DecisionRecorder
}

// NewWarmPoolReconciler builds a reconciler over the given target source, pod
// client, and busy-worker source. busy is REQUIRED: it classifies each live pod as
// busy or idle so scale-down never kills an in-flight attempt (ADR 0058 N1d-b M1);
// without it every worker would look idle and a busy worker could be deleted, so a
// nil busy source (or a busy-list error at tick time) makes the tick do nothing —
// do-no-harm. logger and rec (metrics) are optional — a nil logger falls back to
// the default and a nil rec skips metering.
func NewWarmPoolReconciler(targets WarmTargetSource, pods WarmPodClient, busy BusyWarmWorkerSource, logger *slog.Logger, rec DecisionRecorder) *WarmPoolReconciler {
	if logger == nil {
		logger = slog.Default()
	}
	return &WarmPoolReconciler{targets: targets, pods: pods, busy: busy, logger: logger, rec: rec}
}

// Reconcile brings the warm fleet in line with the active targets for one tick. It
// returns an error only when it could not read the world (the target source or the
// pod list failed) so the ticker logs it and retries next tick without acting on a
// bad view; per-version and per-pod failures are logged/metered and isolated, and
// never abort the sweep.
func (r *WarmPoolReconciler) Reconcile(ctx context.Context) error {
	targets, err := r.targets.ActiveWarmTargets(ctx)
	if err != nil {
		return err
	}
	existing, err := r.pods.ListWarmPods(ctx)
	if err != nil {
		return err
	}

	// Read the busy set ONCE per tick. This is the safety input: it tells the
	// reconciler which live workers are serving a running attempt so scale-down and
	// drain delete only IDLE workers (ADR 0058 N1d-b M1). Without it every worker
	// looks idle and a busy worker could be deleted mid-attempt, so a nil source or
	// a store error here ABORTS the tick with zero creates/deletes (do-no-harm):
	// log, meter, and return so the ticker retries next tick on a good view.
	if r.busy == nil {
		r.logger.ErrorContext(ctx, "warm-pool reconcile skipped: no busy-worker source wired; cannot classify workers, taking no action")
		r.record("warm_pool_busy_source_missing")
		return nil
	}
	busy, err := r.busy.ListBusyWarmWorkerPods(ctx)
	if err != nil {
		r.logger.ErrorContext(ctx, "warm-pool reconcile aborted: listing busy warm workers failed; taking no action this tick to avoid deleting a busy worker", "error", err)
		r.record("warm_pool_busy_list_error")
		return nil
	}

	// Group the existing fleet by the dag_version each worker serves.
	byVersion := make(map[string][]WarmPodInfo, len(existing))
	for _, p := range existing {
		byVersion[p.DagVersionID] = append(byVersion[p.DagVersionID], p)
	}

	// Reconcile every active version to its target, then drain any version that is
	// present in the fleet but no longer active (its target is implicitly 0).
	active := make(map[string]bool, len(targets))
	for _, t := range targets {
		active[t.DagVersionID] = true
		r.reconcileVersion(ctx, t, byVersion[t.DagVersionID], busy)
	}
	for dagVersionID, have := range byVersion {
		if active[dagVersionID] {
			continue
		}
		r.reconcileVersion(ctx, WarmTarget{DagVersionID: dagVersionID, EffectiveMinIdle: 0}, have, busy)
	}
	return nil
}

// reconcileVersion brings one dag_version's IDLE warm-worker buffer to its target
// under model A2 (ADR 0058 N1d-b), isolated behind a recover so a panic in one
// version's create/delete path never takes down the whole sweep. busy is the
// tick's live busy set (pod names serving a running attempt).
//
// The unit here is IDLE workers, not total workers: EffectiveMinIdle is how many
// idle workers to keep ready and MaxPoolSize is the total ceiling. So the pool
// grows past min_idle under load (busy workers do not consume the idle buffer) and
// never exceeds the ceiling, and scale-down/drain deletes only IDLE workers so an
// in-flight attempt is never killed (M1).
func (r *WarmPoolReconciler) reconcileVersion(ctx context.Context, t WarmTarget, have []WarmPodInfo, busy map[string]bool) {
	defer func() {
		if p := recover(); p != nil {
			r.logger.ErrorContext(ctx, "warm-pool reconcile panicked for a dag_version; other versions unaffected",
				"dag_version", t.DagVersionID, "panic", p)
			r.record("warm_pool_version_panic")
		}
	}()

	// Reap every terminal warm pod first and keep only the live ones. Warm pods
	// are RestartPolicy:Never with no agent reconnect, so a Succeeded/Failed pod
	// is a dead worker: it can never serve again, must not count toward the
	// target (or a crashed/drained worker would never be replaced and the pool
	// would silently die), and must not leak. This runs regardless of target, so
	// the drain path (inactive version, target 0) reaps terminal pods too. Then
	// partition the live pods into BUSY (currently serving a running attempt) and
	// IDLE (the rest) — the reconciler only ever creates toward, or deletes from,
	// the IDLE set; busy workers are never touched (M1).
	idle := make([]WarmPodInfo, 0, len(have))
	busyCount := 0
	for _, p := range have {
		if p.Terminal {
			r.deleteWarmPod(ctx, t, p, "deleting terminal warm worker")
			continue
		}
		if busy[p.Name] {
			busyCount++
			continue
		}
		idle = append(idle, p)
	}
	live := busyCount + len(idle)

	// Effective ceiling: the idle buffer must always be creatable, so never let the
	// ceiling fall below the idle target even if an operator (or a stale target)
	// hands us MaxPoolSize < EffectiveMinIdle. In practice MaxPoolSize >=
	// EffectiveMinIdle already (boot validation + upstream clamp), so this is a
	// defensive floor, not a normal path.
	maxPool := t.MaxPoolSize
	if maxPool < t.EffectiveMinIdle {
		maxPool = t.EffectiveMinIdle
	}

	switch {
	case len(idle) < t.EffectiveMinIdle && live < maxPool:
		// Idle buffer is short AND the pool is below its total ceiling: create
		// enough to restore the buffer, but never past the ceiling. create =
		// min(idle shortfall, headroom to the ceiling). Each create is isolated so
		// one apiserver rejection does not stop the rest of this version's workers.
		create := t.EffectiveMinIdle - len(idle)
		if headroom := maxPool - live; headroom < create {
			create = headroom
		}
		for i := 0; i < create; i++ {
			if err := r.pods.CreateWarmPod(ctx, t); err != nil {
				r.logger.ErrorContext(ctx, "creating warm worker",
					"dag_version", t.DagVersionID, "image", t.Image, "error", err)
				r.record("warm_pool_create_error")
				continue
			}
			r.record("warm_pool_pod_created")
		}
	case len(idle) > t.EffectiveMinIdle:
		// Too many idle workers (over target, or the version is no longer active so
		// the idle target is 0): delete the excess IDLE workers only. Busy workers
		// are excluded from `idle` entirely, so a scale-down or a drain can never
		// kill an in-flight attempt (M1) — a busy worker is deleted only on a later
		// tick, once it has gone idle. Deleting the tail is arbitrary but stable:
		// every idle worker of a version is interchangeable.
		for _, p := range idle[t.EffectiveMinIdle:] {
			r.deleteWarmPod(ctx, t, p, "deleting excess idle warm worker")
		}
	}
}

// deleteWarmPod deletes one warm worker by name, logging/metering a failure
// without aborting the sweep. msg names why (terminal cleanup vs excess).
func (r *WarmPoolReconciler) deleteWarmPod(ctx context.Context, t WarmTarget, p WarmPodInfo, msg string) {
	if err := r.pods.DeleteWarmPod(ctx, p.Name); err != nil {
		r.logger.ErrorContext(ctx, msg,
			"dag_version", t.DagVersionID, "pod", p.Name, "error", err)
		r.record("warm_pool_delete_error")
		return
	}
	r.record("warm_pool_pod_deleted")
}

func (r *WarmPoolReconciler) record(decision string) {
	if r.rec != nil {
		r.rec.RecordSchedulerDecision(decision)
	}
}
