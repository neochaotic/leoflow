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

func warmPods(dagVersionID string, names ...string) []WarmPodInfo {
	out := make([]WarmPodInfo, 0, len(names))
	for _, n := range names {
		out = append(out, WarmPodInfo{Name: n, DagVersionID: dagVersionID})
	}
	return out
}

func reconcileWarm(t *testing.T, targets *fakeWarmTargets, pods *fakeWarmPods) {
	t.Helper()
	r := NewWarmPoolReconciler(targets, pods, nil, nil)
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
	r := NewWarmPoolReconciler(targets, pods, nil, nil)
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
	r := NewWarmPoolReconciler(targets, pods, nil, nil)
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile = nil on targets error, want the error surfaced")
	}
}
