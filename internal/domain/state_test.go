package domain

import "testing"

func TestTaskStateIsTerminal(t *testing.T) {
	terminal := map[TaskState]bool{
		TaskStateNone:           false,
		TaskStateScheduled:      false,
		TaskStateQueued:         false,
		TaskStateRunning:        false,
		TaskStateUpForRetry:     false,
		TaskStateSuccess:        true,
		TaskStateFailed:         true,
		TaskStateSkipped:        true,
		TaskStateUpstreamFailed: true,
	}
	for state, want := range terminal {
		if got := state.IsTerminal(); got != want {
			t.Errorf("%s.IsTerminal() = %v, want %v", state, got, want)
		}
	}
}

func TestDagRunStateIsTerminal(t *testing.T) {
	terminal := map[DagRunState]bool{
		DagRunStateQueued:  false,
		DagRunStateRunning: false,
		DagRunStateSuccess: true,
		DagRunStateFailed:  true,
	}
	for state, want := range terminal {
		if got := state.IsTerminal(); got != want {
			t.Errorf("%s.IsTerminal() = %v, want %v", state, got, want)
		}
	}
}
