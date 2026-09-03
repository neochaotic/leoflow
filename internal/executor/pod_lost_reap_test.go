package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakePodLostStore serves running-TI candidates and records pod_lost marks.
type fakePodLostStore struct {
	candidates []PodLostCandidate
	listErr    error
	marked     []string
	markErr    error
	markNoop   bool // when true, MarkTaskPodLost reports 0 rows updated (a late terminal report won the race)
}

func (f *fakePodLostStore) ListRunningTasks(context.Context) ([]PodLostCandidate, error) {
	return f.candidates, f.listErr
}

func (f *fakePodLostStore) MarkTaskPodLost(_ context.Context, id string) (bool, error) {
	if f.markErr != nil {
		return false, f.markErr
	}
	if f.markNoop {
		return false, nil
	}
	f.marked = append(f.marked, id)
	return true, nil
}

func TestIsPodLostCandidate(t *testing.T) {
	const grace = 60 * time.Second
	now := time.Now().UTC()
	cases := []struct {
		name  string
		since time.Time
		want  bool
	}{
		{"zero running-since is alive (do no harm)", time.Time{}, false},
		{"within grace is not yet a candidate", now.Add(-30 * time.Second), false},
		{"exactly at grace is a candidate", now.Add(-60 * time.Second), true},
		{"well past grace is a candidate", now.Add(-5 * time.Minute), true},
		{"future running-since (clock skew) is alive", now.Add(1 * time.Second), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPodLostCandidate(PodLostCandidate{RunningSince: tc.since}, grace, now); got != tc.want {
				t.Errorf("IsPodLostCandidate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodLostReaper(t *testing.T) {
	const grace = 60 * time.Second
	now := time.Now().UTC()
	past := now.Add(-2 * time.Minute) // comfortably past the grace period

	// newReaper wires a K8s reaper (pods set); the Lite case sets pods=nil.
	newReaper := func(store PodLostReapStore, pods PodManager) *podLostReaper {
		r := newPodLostReaper(store, reapTestLogger(), grace, nil)
		r.pods = pods
		return r
	}

	t.Run("running TI past grace with no live pod is failed as pod_lost + torn down", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{
			{TaskInstanceID: "lost", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
		}}
		pods := &fakePodManager{active: map[string]bool{}} // no live pod for run-a/work
		if err := newReaper(store, pods).run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 1 || store.marked[0] != "lost" {
			t.Fatalf("expected TI 'lost' marked pod_lost, got %v", store.marked)
		}
		if len(pods.deletedTasks) != 1 || pods.deletedTasks[0] != (deletedTask{"run-a", "work", 1}) {
			t.Fatalf("expected best-effort teardown of (run-a,work,try 1), got %v", pods.deletedTasks)
		}
	})

	t.Run("a live pod defers — never reaped", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{
			{TaskInstanceID: "live", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
		}}
		pods := &fakePodManager{active: map[string]bool{"run-a/work/1": true}}
		if err := newReaper(store, pods).run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 0 {
			t.Fatalf("a running TI with a live pod must not be reaped, got %v", store.marked)
		}
	})

	t.Run("within the grace period is not reaped (do no harm)", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{
			{TaskInstanceID: "fresh", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: now.Add(-10 * time.Second)},
		}}
		pods := &fakePodManager{active: map[string]bool{}}
		if err := newReaper(store, pods).run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 0 {
			t.Fatalf("a freshly-running TI must not be reaped, got %v", store.marked)
		}
	})

	t.Run("pod liveness query error defers (do no harm)", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{
			{TaskInstanceID: "unknown", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
		}}
		pods := &fakePodManager{activeErr: errors.New("apiserver unavailable")}
		if err := newReaper(store, pods).run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 0 {
			t.Fatalf("liveness-unknown must defer, got %v", store.marked)
		}
	})

	t.Run("Lite (nil PodManager) is a no-op — never reaps a subprocess task", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{
			{TaskInstanceID: "subproc", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
		}}
		r := newPodLostReaper(store, reapTestLogger(), grace, nil) // r.pods stays nil
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 0 {
			t.Fatalf("Lite must not reap on 'no pod' (there are no pods), got %v", store.marked)
		}
	})

	t.Run("a no-op mark (late terminal report won) skips the teardown", func(t *testing.T) {
		store := &fakePodLostStore{
			candidates: []PodLostCandidate{
				{TaskInstanceID: "raced", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
			},
			markNoop: true,
		}
		pods := &fakePodManager{active: map[string]bool{}} // no live pod → would reap, but the mark no-ops
		if err := newReaper(store, pods).run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(pods.deletedTasks) != 0 {
			t.Fatalf("a no-op mark (0 rows) must not tear down a pod, got %v", pods.deletedTasks)
		}
	})

	t.Run("post-leadership grace defers, then reaps once it elapses", func(t *testing.T) {
		// A control-plane restart manufactures the very signal this reaper acts
		// on: a task pod that finished DURING the outage is no longer live, yet
		// its TI is still `running` because its terminal report never landed.
		// The pod's durable outcome record is recovered by the reconciler on its
		// slower sweep — so the new leader must hold off pod-lost reaping until
		// the reconciler has had time to settle the true outcome. Within the
		// grace: NO MarkTaskPodLost, a recorded skip. Past it: reap as usual.
		newStore := func() *fakePodLostStore {
			return &fakePodLostStore{candidates: []PodLostCandidate{
				{TaskInstanceID: "finished-during-outage", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
			}}
		}
		within := newStore()
		rec := &capturingRecorder{}
		pods := &fakePodManager{active: map[string]bool{}} // pod gone (Succeeded, not Pending/Running)
		r := newPodLostReaper(within, reapTestLogger(), grace, rec)
		r.pods = pods
		r.leaderGrace = 180 * time.Second
		r.leaderSince = func() time.Time { return now.Add(-10 * time.Second) } // just became leader
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(within.marked) != 0 {
			t.Fatalf("must not mark pod_lost within the post-leadership grace, got %v", within.marked)
		}
		if len(pods.deletedTasks) != 0 {
			t.Fatalf("must not tear down a pod within the post-leadership grace, got %v", pods.deletedTasks)
		}
		if got := rec.count("pod_lost_grace_skip"); got != 1 {
			t.Errorf("pod_lost_grace_skip = %d, want 1", got)
		}

		pastGrace := newStore()
		r2 := newPodLostReaper(pastGrace, reapTestLogger(), grace, &capturingRecorder{})
		r2.pods = &fakePodManager{active: map[string]bool{}}
		r2.leaderGrace = 180 * time.Second
		r2.leaderSince = func() time.Time { return now.Add(-10 * time.Minute) } // leader long enough
		if err := r2.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(pastGrace.marked) != 1 || pastGrace.marked[0] != "finished-during-outage" {
			t.Fatalf("must reap once the post-leadership grace elapsed, got %v", pastGrace.marked)
		}
	})

	t.Run("nil leaderSince (Lite / no leadership) leaves the grace off", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{
			{TaskInstanceID: "lost", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
		}}
		r := newPodLostReaper(store, reapTestLogger(), grace, nil)
		r.pods = &fakePodManager{active: map[string]bool{}}
		r.leaderGrace = 180 * time.Second // configured, but no leadership accessor
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 1 {
			t.Fatalf("without a leaderSince accessor the reaper must behave as before, got %v", store.marked)
		}
	})

	t.Run("a list error surfaces and reaps nothing", func(t *testing.T) {
		store := &fakePodLostStore{listErr: errors.New("db down")}
		pods := &fakePodManager{active: map[string]bool{}}
		if err := newReaper(store, pods).run(context.Background()); err == nil {
			t.Fatal("expected the list error to surface")
		}
		if len(store.marked) != 0 {
			t.Fatalf("nothing should be reaped on a list error, got %v", store.marked)
		}
	})
}
