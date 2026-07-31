package scheduler

import "testing"

// IsLeading must reflect SetLeading, so background sweeps (pod reconciler,
// staging GC) can gate on the same leadership signal the scheduler loop uses —
// otherwise at replicaCount>1 every replica sweeps and deletes.
func TestIsLeadingReflectsSetLeading(t *testing.T) {
	s := &Scheduler{}
	if s.IsLeading() {
		t.Error("a fresh scheduler is not leading")
	}
	s.SetLeading(true)
	if !s.IsLeading() {
		t.Error("IsLeading should be true after SetLeading(true)")
	}
	s.SetLeading(false)
	if s.IsLeading() {
		t.Error("IsLeading should be false after SetLeading(false)")
	}
}
