package main

import (
	"testing"

	"github.com/neochaotic/leoflow/internal/executor"
)

// TestResilienceLadderWiringValidates pins that the ladder the server actually
// boots with — agent heartbeat/TTL, default reaper config, reconcile interval —
// satisfies every ordering the restart recovery depends on. A change to any one
// of those constants that breaks the order fails this test before it fails a
// deployment at boot.
func TestResilienceLadderWiringValidates(t *testing.T) {
	l := resilienceLadder()
	if err := executor.ValidateResilienceLadder(l); err != nil {
		t.Fatalf("server ladder %+v must validate: %v", l, err)
	}
	if l.ReconcileInterval != reconcileInterval || l.AttemptTokenTTL != attemptTokenTTL {
		t.Errorf("ladder must carry the wired values: %+v", l)
	}
}
