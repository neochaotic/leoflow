package executor

import (
	"context"
	"log/slog"
)

// WarmTarget is one active dag_version the warm-pool reconciler keeps warm workers
// ready for, and how many (already resolved through the clamp/fallback/flag gate —
// see config.ExecutionSection.EffectiveMinIdle). Image is the DAG's image the warm
// worker runs the agent from.
type WarmTarget struct {
	DagVersionID     string
	Image            string
	EffectiveMinIdle int
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

// WarmPodInfo identifies one existing warm-worker pod and the dag_version it
// serves (read from its labels). The reconciler counts these per version.
type WarmPodInfo struct {
	Name         string
	DagVersionID string
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

// WarmPoolReconciler maintains the target number of warm workers per active
// dag_version (ADR 0058 N1b2b, model A2). Each tick it reads the active targets
// and the existing warm fleet, then for each version CREATEs missing workers up to
// the target and DELETEs excess — and drains any version that is no longer active
// (target 0). It is leader-gated (run on a gated ticker, like the pod reconciler)
// so at replicaCount>1 only the leader mutates the fleet.
//
// Idempotent (it reconciles to a count, so re-running converges), panic-safe and
// per-version isolated (one bad dag_version never blocks the others), and O(active
// versions) per tick. It is only constructed and started when warm pools are
// enabled; with them off the reconciler never runs, so every warm pool stays empty
// and dispatch is byte-for-byte today's dedicated pod-per-task.
//
// Deferred to N1d (NOT this brick): the idle-TTL freshness recycle (D6), the D9
// worker lifetime / attempts-per-worker caps, and the D7 failover reaper fan-out +
// H2 durable binding. The GC-anchor ConfigMap (D11) is also N1d; a bare warm pod
// deleted here is fine until then.
type WarmPoolReconciler struct {
	targets WarmTargetSource
	pods    WarmPodClient
	logger  *slog.Logger
	rec     DecisionRecorder
}

// NewWarmPoolReconciler builds a reconciler over the given target source and pod
// client. logger and rec (metrics) are optional — a nil logger falls back to the
// default and a nil rec skips metering.
func NewWarmPoolReconciler(targets WarmTargetSource, pods WarmPodClient, logger *slog.Logger, rec DecisionRecorder) *WarmPoolReconciler {
	if logger == nil {
		logger = slog.Default()
	}
	return &WarmPoolReconciler{targets: targets, pods: pods, logger: logger, rec: rec}
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
		r.reconcileVersion(ctx, t, byVersion[t.DagVersionID])
	}
	for dagVersionID, have := range byVersion {
		if active[dagVersionID] {
			continue
		}
		r.reconcileVersion(ctx, WarmTarget{DagVersionID: dagVersionID, EffectiveMinIdle: 0}, have)
	}
	return nil
}

// reconcileVersion brings one dag_version's warm-worker count to its target,
// isolated behind a recover so a panic in one version's create/delete path never
// takes down the whole sweep.
func (r *WarmPoolReconciler) reconcileVersion(ctx context.Context, t WarmTarget, have []WarmPodInfo) {
	defer func() {
		if p := recover(); p != nil {
			r.logger.ErrorContext(ctx, "warm-pool reconcile panicked for a dag_version; other versions unaffected",
				"dag_version", t.DagVersionID, "panic", p)
			r.record("warm_pool_version_panic")
		}
	}()

	switch {
	case len(have) < t.EffectiveMinIdle:
		// Under target: create the shortfall. Each create is isolated so one
		// apiserver rejection does not stop the rest of this version's workers.
		for i := 0; i < t.EffectiveMinIdle-len(have); i++ {
			if err := r.pods.CreateWarmPod(ctx, t); err != nil {
				r.logger.ErrorContext(ctx, "creating warm worker",
					"dag_version", t.DagVersionID, "image", t.Image, "error", err)
				r.record("warm_pool_create_error")
				continue
			}
			r.record("warm_pool_pod_created")
		}
	case len(have) > t.EffectiveMinIdle:
		// Over target (or the version is no longer active, target 0): delete the
		// excess. Deleting the tail is arbitrary but stable — every worker of a
		// version is interchangeable.
		for _, p := range have[t.EffectiveMinIdle:] {
			if err := r.pods.DeleteWarmPod(ctx, p.Name); err != nil {
				r.logger.ErrorContext(ctx, "deleting excess warm worker",
					"dag_version", t.DagVersionID, "pod", p.Name, "error", err)
				r.record("warm_pool_delete_error")
				continue
			}
			r.record("warm_pool_pod_deleted")
		}
	}
}

func (r *WarmPoolReconciler) record(decision string) {
	if r.rec != nil {
		r.rec.RecordSchedulerDecision(decision)
	}
}
