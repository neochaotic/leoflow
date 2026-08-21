package executor

import (
	"context"
	"errors"
	"testing"
)

// fakeWarmTargets is a canned WarmTargetSource.
type fakeWarmTargets struct {
	targets []WarmTarget
	err     error
}

func (f *fakeWarmTargets) ActiveWarmTargets(context.Context) ([]WarmTarget, error) {
	return f.targets, f.err
}

// fakeWarmPods records the reconciler's create/delete calls against a canned
// existing-pod set, so a test can assert exactly what the reconciler did.
type fakeWarmPods struct {
	existing  []WarmPodInfo
	created   []WarmTarget
	deleted   []string
	listErr   error
	createErr map[string]error // per dag_version create error (nil = ok)
	panicOn   string           // dag_version whose create panics (isolation test)
}

func (f *fakeWarmPods) ListWarmPods(context.Context) ([]WarmPodInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.existing, nil
}

func (f *fakeWarmPods) CreateWarmPod(_ context.Context, t WarmTarget) error {
	if f.panicOn != "" && t.DagVersionID == f.panicOn {
		panic("boom creating " + t.DagVersionID)
	}
	if f.createErr != nil {
		if err := f.createErr[t.DagVersionID]; err != nil {
			return err
		}
	}
	f.created = append(f.created, t)
	return nil
}

func (f *fakeWarmPods) DeleteWarmPod(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}

// fakeBusyWorkers is a canned BusyWarmWorkerSource: the set of warm-worker pod
// names currently serving a running attempt. err simulates the busy-list failing
// (do-no-harm abort).
type fakeBusyWorkers struct {
	busy map[string]bool
	err  error
}

func (f *fakeBusyWorkers) ListBusyWarmWorkerPods(context.Context) (map[string]bool, error) {
	return f.busy, f.err
}

// busySet builds a busy source from a list of busy pod names.
func busySet(names ...string) *fakeBusyWorkers {
	b := map[string]bool{}
	for _, n := range names {
		b[n] = true
	}
	return &fakeBusyWorkers{busy: b}
}

func warmPods(dagVersionID string, names ...string) []WarmPodInfo {
	out := make([]WarmPodInfo, 0, len(names))
	for _, n := range names {
		out = append(out, WarmPodInfo{Name: n, DagVersionID: dagVersionID})
	}
	return out
}

// warmTerminalPods builds warm pods that have reached a terminal phase (a
// crashed/drained/finished RestartPolicy:Never worker). The reconciler must not
// count these toward the target and must always reap them.
func warmTerminalPods(dagVersionID string, names ...string) []WarmPodInfo {
	out := make([]WarmPodInfo, 0, len(names))
	for _, n := range names {
		out = append(out, WarmPodInfo{Name: n, DagVersionID: dagVersionID, Terminal: true})
	}
	return out
}

// reconcileWarm runs a tick with NO busy workers (every live pod is idle) — the
// classic case the pre-N1d-b tests exercise.
func reconcileWarm(t *testing.T, targets *fakeWarmTargets, pods *fakeWarmPods) {
	t.Helper()
	reconcileWarmBusy(t, targets, pods, &fakeBusyWorkers{})
}

// reconcileWarmBusy runs a tick against a specific busy set.
func reconcileWarmBusy(t *testing.T, targets *fakeWarmTargets, pods *fakeWarmPods, busy *fakeBusyWorkers) {
	t.Helper()
	r := NewWarmPoolReconciler(targets, pods, busy, nil, nil)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// TestWarmPoolReconcileCreatesMissing: target 2, zero existing => create 2.
func TestWarmPoolReconcileCreatesMissing(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2}}}
	pods := &fakeWarmPods{}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 2 {
		t.Errorf("created %d pods, want 2", len(pods.created))
	}
	for _, c := range pods.created {
		if c.DagVersionID != "dv1" || c.Image != "img" {
			t.Errorf("created %+v, want dv1/img", c)
		}
	}
	if len(pods.deleted) != 0 {
		t.Errorf("deleted %v, want none", pods.deleted)
	}
}

// TestWarmPoolReconcileAtTargetNoop: target 2, 2 existing => create 0, delete 0.
func TestWarmPoolReconcileAtTargetNoop(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "p1", "p2")}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 0 || len(pods.deleted) != 0 {
		t.Errorf("created %v deleted %v, want no changes", pods.created, pods.deleted)
	}
}

// TestWarmPoolReconcileDeletesExcess: target 2, 3 existing => delete 1.
func TestWarmPoolReconcileDeletesExcess(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "p1", "p2", "p3")}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none", pods.created)
	}
	if len(pods.deleted) != 1 {
		t.Fatalf("deleted %v, want exactly 1", pods.deleted)
	}
}

// TestWarmPoolReconcileDeletesInactiveVersion: a version no longer active (not in
// targets) with 1 existing pod => the pod is deleted (target 0).
func TestWarmPoolReconcileDeletesInactiveVersion(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 1}}}
	pods := &fakeWarmPods{existing: append(warmPods("dv1", "p1"), warmPods("gone", "orphan")...)}
	reconcileWarm(t, targets, pods)
	if len(pods.deleted) != 1 || pods.deleted[0] != "orphan" {
		t.Errorf("deleted %v, want [orphan] (the inactive version's pod)", pods.deleted)
	}
	// dv1 was already at its target of 1, so no create.
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none", pods.created)
	}
}

// TestWarmPoolReconcileTargetZeroCreatesNothing: warm pools off / effective target
// 0 (the reconciler receives no targets, or a target of 0) creates nothing and,
// for a 0-target with existing pods, drains them.
func TestWarmPoolReconcileTargetZeroCreatesNothing(t *testing.T) {
	// No active targets at all: nothing to create, nothing existing to delete.
	reconcileWarm(t, &fakeWarmTargets{}, &fakeWarmPods{})

	// Explicit zero target with existing pods => drained to zero, never created.
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 0}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "p1", "p2")}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none for a zero target", pods.created)
	}
	if len(pods.deleted) != 2 {
		t.Errorf("deleted %v, want both pods drained for a zero target", pods.deleted)
	}
}

// TestWarmPoolReconcileReplacesTerminalPods is the H1 regression: a fleet of 2
// TERMINAL pods (crashed/drained workers) for a target of 2 must NOT read as
// satisfied. The reconciler must CREATE 2 replacements (terminal pods do not
// count toward the target) AND reap the 2 terminal pods (they can never serve
// again). Before the fix this created 0 and deleted 0 — the pool silently died.
func TestWarmPoolReconcileReplacesTerminalPods(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2}}}
	pods := &fakeWarmPods{existing: warmTerminalPods("dv1", "dead1", "dead2")}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 2 {
		t.Errorf("created %d pods, want 2 (terminal pods must not count toward the target)", len(pods.created))
	}
	if len(pods.deleted) != 2 {
		t.Errorf("deleted %v, want the 2 terminal pods reaped", pods.deleted)
	}
	gotDeleted := map[string]bool{}
	for _, n := range pods.deleted {
		gotDeleted[n] = true
	}
	if !gotDeleted["dead1"] || !gotDeleted["dead2"] {
		t.Errorf("deleted %v, want both dead1 and dead2 reaped", pods.deleted)
	}
}

// TestWarmPoolReconcileTerminalPlusLive: target 2 with 1 live + 1 terminal =>
// create 1 (only the live pod counts), reap the terminal one, keep the live one.
func TestWarmPoolReconcileTerminalPlusLive(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2}}}
	pods := &fakeWarmPods{existing: append(warmPods("dv1", "live1"), warmTerminalPods("dv1", "dead1")...)}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 1 {
		t.Errorf("created %d pods, want 1 (1 live + 1 terminal, only live counts)", len(pods.created))
	}
	if len(pods.deleted) != 1 || pods.deleted[0] != "dead1" {
		t.Errorf("deleted %v, want [dead1] (terminal reaped, live kept)", pods.deleted)
	}
}

// TestWarmPoolReconcileDrainsTerminalOnInactiveVersion: an inactive version
// (target 0) with a live + a terminal pod must drain BOTH.
func TestWarmPoolReconcileDrainsTerminalOnInactiveVersion(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 1}}}
	pods := &fakeWarmPods{existing: append(
		warmPods("dv1", "keep"),
		append(warmPods("gone", "orphan-live"), warmTerminalPods("gone", "orphan-dead")...)...,
	)}
	reconcileWarm(t, targets, pods)
	gotDeleted := map[string]bool{}
	for _, n := range pods.deleted {
		gotDeleted[n] = true
	}
	if !gotDeleted["orphan-live"] || !gotDeleted["orphan-dead"] {
		t.Errorf("deleted %v, want both orphan-live and orphan-dead drained", pods.deleted)
	}
	if gotDeleted["keep"] {
		t.Errorf("deleted %v, must not drain the active version's live pod", pods.deleted)
	}
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none (dv1 already at target)", pods.created)
	}
}

// TestWarmPoolReconcilePerVersionIsolation: a version whose create panics must not
// block a healthy version's reconcile (panic-safe, per-version isolated).
func TestWarmPoolReconcilePerVersionIsolation(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "bad", EffectiveMinIdle: 1},
		{DagVersionID: "good", EffectiveMinIdle: 2},
	}}
	pods := &fakeWarmPods{panicOn: "bad"}
	// Must not panic out of Reconcile.
	reconcileWarm(t, targets, pods)
	// The healthy version was still fully reconciled.
	var good int
	for _, c := range pods.created {
		if c.DagVersionID == "good" {
			good++
		}
	}
	if good != 2 {
		t.Errorf("healthy version got %d creates, want 2 (bad version must not block it)", good)
	}
}

// TestWarmPoolReconcileCreateErrorIsolatedPerPod: a create error is logged and the
// version keeps going; it does not abort the whole reconcile.
func TestWarmPoolReconcileCreateErrorIsolatedPerPod(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "err", EffectiveMinIdle: 2},
		{DagVersionID: "ok", EffectiveMinIdle: 1},
	}}
	pods := &fakeWarmPods{createErr: map[string]error{"err": errors.New("apiserver 500")}}
	reconcileWarm(t, targets, pods)
	// The healthy version still created its pod despite the other's error.
	var ok int
	for _, c := range pods.created {
		if c.DagVersionID == "ok" {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("ok version got %d creates, want 1", ok)
	}
}

// TestWarmPoolReconcileListErrorReturned: a list failure is surfaced so the ticker
// logs it and retries next tick (nothing is created off a bad view of the world).
func TestWarmPoolReconcileListErrorReturned(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 1}}}
	pods := &fakeWarmPods{listErr: errors.New("watch broke")}
	r := NewWarmPoolReconciler(targets, pods, &fakeBusyWorkers{}, nil, nil)
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile = nil on list error, want the error surfaced")
	}
	if len(pods.created) != 0 {
		t.Errorf("created %v off a failed list, want none", pods.created)
	}
}

// TestWarmPoolReconcileTargetsErrorReturned: a targets-source failure is surfaced.
func TestWarmPoolReconcileTargetsErrorReturned(t *testing.T) {
	targets := &fakeWarmTargets{err: errors.New("db down")}
	pods := &fakeWarmPods{}
	r := NewWarmPoolReconciler(targets, pods, &fakeBusyWorkers{}, nil, nil)
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile = nil on targets error, want the error surfaced")
	}
}

// ---- ADR 0058 N1d-b: busy-aware reconcile (model A2). EffectiveMinIdle is the
// number of IDLE workers to keep ready; MaxPoolSize is the total ceiling. Busy
// workers (serving a running attempt) never count against the idle target and are
// never deleted. ----

// deletedSet turns the reconciler's deleted list into a set for membership asserts.
func deletedSet(pods *fakeWarmPods) map[string]bool {
	s := map[string]bool{}
	for _, n := range pods.deleted {
		s[n] = true
	}
	return s
}

// TestWarmPoolReconcileCreatesIdleBufferUnderLoad is the M2 create-idle-buffer
// case: EffectiveMinIdle=2, MaxPoolSize=8, and all 3 live workers are BUSY (0
// idle). Model A2 keeps 2 IDLE workers ready, so busy workers must NOT satisfy the
// idle target: the reconciler CREATES 2 (restoring the idle buffer) and deletes 0.
// The pre-N1d-b logic counted total workers against the target (3 > 2) and deleted
// a busy worker — the M1/M2 bug this fix closes.
func TestWarmPoolReconcileCreatesIdleBufferUnderLoad(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "b1", "b2", "b3")}
	reconcileWarmBusy(t, targets, pods, busySet("b1", "b2", "b3"))
	if len(pods.created) != 2 {
		t.Errorf("created %d, want 2 (busy workers do not count toward the idle buffer)", len(pods.created))
	}
	if len(pods.deleted) != 0 {
		t.Errorf("deleted %v, want none (all 3 live workers are busy)", pods.deleted)
	}
}

// TestWarmPoolReconcileRespectsMaxPoolSize is the M2 respect-ceiling case:
// EffectiveMinIdle=2, MaxPoolSize=8, 8 live workers all BUSY (0 idle). The idle
// buffer is short but the pool is at its total ceiling, so the reconciler creates
// 0 (never exceeding MaxPoolSize) and deletes 0.
func TestWarmPoolReconcileRespectsMaxPoolSize(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "b1", "b2", "b3", "b4", "b5", "b6", "b7", "b8")}
	reconcileWarmBusy(t, targets, pods, busySet("b1", "b2", "b3", "b4", "b5", "b6", "b7", "b8"))
	if len(pods.created) != 0 {
		t.Errorf("created %d, want 0 (pool is at MaxPoolSize=8)", len(pods.created))
	}
	if len(pods.deleted) != 0 {
		t.Errorf("deleted %v, want none", pods.deleted)
	}
}

// TestWarmPoolReconcileIdleSteadyState: EffectiveMinIdle=2 with exactly 2 idle + 3
// busy workers is at steady state — the idle buffer is satisfied and no idle is in
// excess, so nothing is created or deleted.
func TestWarmPoolReconcileIdleSteadyState(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "i1", "i2", "b1", "b2", "b3")}
	reconcileWarmBusy(t, targets, pods, busySet("b1", "b2", "b3"))
	if len(pods.created) != 0 || len(pods.deleted) != 0 {
		t.Errorf("created %v deleted %v, want no changes (2 idle + 3 busy is steady state)", pods.created, pods.deleted)
	}
}

// TestWarmPoolReconcileDeletesExcessIdleOnly: EffectiveMinIdle=2 with 5 idle + 1
// busy. There are 3 idle workers over target, so the reconciler deletes exactly 3
// IDLE workers, keeping the busy worker and 2 idle.
func TestWarmPoolReconcileDeletesExcessIdleOnly(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "i1", "i2", "i3", "i4", "i5", "b1")}
	reconcileWarmBusy(t, targets, pods, busySet("b1"))
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none", pods.created)
	}
	if len(pods.deleted) != 3 {
		t.Fatalf("deleted %v, want exactly 3 idle workers", pods.deleted)
	}
	if deletedSet(pods)["b1"] {
		t.Errorf("deleted %v, must NOT delete the busy worker b1", pods.deleted)
	}
}

// TestWarmPoolReconcileNeverDeletesBusy is the M1 case: a scale-down where every
// candidate is BUSY. EffectiveMinIdle=0 (drain-like) with 3 busy workers and 0
// idle — there is nothing idle to delete, so the reconciler deletes 0. A busy
// worker's in-flight attempt is never killed; it is reaped on a later tick once it
// goes idle.
func TestWarmPoolReconcileNeverDeletesBusy(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 0, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "b1", "b2", "b3")}
	reconcileWarmBusy(t, targets, pods, busySet("b1", "b2", "b3"))
	if len(pods.deleted) != 0 {
		t.Errorf("deleted %v, want none (every candidate is busy; M1: never delete a busy worker)", pods.deleted)
	}
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none", pods.created)
	}
}

// TestWarmPoolReconcileDrainLeavesBusy is the drain case: an inactive version
// (target 0, MaxPoolSize 0) holding 2 idle + 1 busy + 1 terminal. The reconciler
// drains the 2 idle and reaps the 1 terminal, but LEAVES the busy worker to finish
// its attempt — deleting it would kill live work (M1). It is deleted next tick
// once idle.
func TestWarmPoolReconcileDrainLeavesBusy(t *testing.T) {
	// dv1 is active (keeps its own pod); "gone" is inactive (not in targets).
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 1, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: append(
		warmPods("dv1", "keep"),
		append(warmPods("gone", "idle1", "idle2", "busy1"), warmTerminalPods("gone", "dead1")...)...,
	)}
	reconcileWarmBusy(t, targets, pods, busySet("busy1"))
	del := deletedSet(pods)
	if !del["idle1"] || !del["idle2"] {
		t.Errorf("deleted %v, want both idle workers drained", pods.deleted)
	}
	if !del["dead1"] {
		t.Errorf("deleted %v, want the terminal pod reaped", pods.deleted)
	}
	if del["busy1"] {
		t.Errorf("deleted %v, must LEAVE the busy worker to finish its attempt (M1)", pods.deleted)
	}
	if del["keep"] {
		t.Errorf("deleted %v, must not touch the active version's pod", pods.deleted)
	}
	if len(pods.deleted) != 3 {
		t.Errorf("deleted %v, want exactly 3 (2 idle + 1 terminal)", pods.deleted)
	}
}

// TestWarmPoolReconcileBusyListErrorAborts is the do-no-harm case: the busy-worker
// source erroring means the reconciler cannot tell busy from idle, so the tick
// takes NO action — zero creates, zero deletes — rather than risk deleting a busy
// worker. Reconcile returns nil (logged + metered, not surfaced as a ticker error)
// so it simply retries next tick on a good view.
func TestWarmPoolReconcileBusyListErrorAborts(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "p1", "p2", "p3")}
	busy := &fakeBusyWorkers{err: errors.New("db down")}
	r := NewWarmPoolReconciler(targets, pods, busy, nil, nil)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile = %v, want nil (busy-list error is do-no-harm, not surfaced)", err)
	}
	if len(pods.created) != 0 || len(pods.deleted) != 0 {
		t.Errorf("created %v deleted %v, want NO action when the busy set is unavailable", pods.created, pods.deleted)
	}
}

// TestWarmPoolReconcileNoBusySourceAborts: a nil busy source (wiring regression)
// is do-no-harm too — no classification means no action.
func TestWarmPoolReconcileNoBusySourceAborts(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "p1", "p2", "p3")}
	r := NewWarmPoolReconciler(targets, pods, nil, nil, nil)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile = %v, want nil (nil busy source is do-no-harm)", err)
	}
	if len(pods.created) != 0 || len(pods.deleted) != 0 {
		t.Errorf("created %v deleted %v, want NO action without a busy source", pods.created, pods.deleted)
	}
}
