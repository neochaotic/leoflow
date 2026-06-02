package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// recordingHandler captures every slog Record so a test can assert level +
// message + key attributes. It is package-local because slog/slogtest doesn't
// give us a per-record list; we only need the bare minimum.
type recordingHandler struct {
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }

// TestLogSchedulerErrorDowngradesCanceled pins the contract: a context.Canceled
// error (or any error wrapping it) is logged at WARN, not ERROR. The trigger
// is a leader step-down — the scheduler cancels its run-context as part of the
// graceful loss-of-lock path, and the reapers / scheduler-step that were
// mid-flight return `context canceled`. That is **expected**, not a failure;
// surfacing it as ERROR fires false alerts (#311 was filed by an operator
// seeing exactly this on GKE Pro). A genuine failure (any other error) still
// reaches ERROR so real problems are not muted.
func TestLogSchedulerErrorDowngradesCanceled(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		inStepDown bool
		wantLvl    slog.Level
	}{
		{
			name:       "ctx-canceled INSIDE step-down → WARN (expected fan-out)",
			err:        context.Canceled,
			inStepDown: true,
			wantLvl:    slog.LevelWarn,
		},
		{
			name:       "fmt.Errorf wrap of ctx-canceled INSIDE step-down → WARN",
			err:        fmt.Errorf("listing orphan candidates: %w", context.Canceled),
			inStepDown: true,
			wantLvl:    slog.LevelWarn,
		},
		{
			name:       "ctx-canceled OUTSIDE step-down → ERROR (tripwire — unexpected cancel)",
			err:        context.Canceled,
			inStepDown: false,
			wantLvl:    slog.LevelError,
		},
		{
			name:       "wrapped ctx-canceled OUTSIDE step-down → ERROR (tripwire holds for wraps too)",
			err:        fmt.Errorf("listing orphan candidates: %w", context.Canceled),
			inStepDown: false,
			wantLvl:    slog.LevelError,
		},
		{
			name:       "DeadlineExceeded INSIDE step-down → ERROR (a real stall, not a deliberate cancel)",
			err:        context.DeadlineExceeded,
			inStepDown: true,
			wantLvl:    slog.LevelError,
		},
		{
			name:       "generic infra error → ERROR (regardless of step-down state)",
			err:        errors.New("postgres connection refused"),
			inStepDown: true,
			wantLvl:    slog.LevelError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &recordingHandler{}
			logSchedulerError(slog.New(h), "orphan reaper", tc.err, tc.inStepDown)
			if len(h.records) != 1 {
				t.Fatalf("expected 1 record, got %d", len(h.records))
			}
			if got := h.records[0].Level; got != tc.wantLvl {
				t.Errorf("level = %v, want %v (err=%v inStepDown=%v)", got, tc.wantLvl, tc.err, tc.inStepDown)
			}
			if !strings.Contains(h.records[0].Message, "orphan reaper") {
				t.Errorf("message %q lost the caller's prefix", h.records[0].Message)
			}
		})
	}
}

// TestMarkSteppingDownIncrementsCounter pins the step-down counter contract.
// Operators alert on `rate(leoflow_scheduler_step_downs_total[5m])`, so this
// test guards that MarkSteppingDown actually emits exactly one count per
// step-down (no double-count under repeated calls — the counter is the rate of
// step-down EVENTS, not of state-flag-flips) and that the reason label is
// preserved verbatim.
func TestMarkSteppingDownIncrementsCounter(t *testing.T) {
	rec := &fakeRecorder{}
	s := NewScheduler(nil, slog.New(&recordingHandler{}), time.Second)
	s.SetRecorder(rec)

	s.MarkSteppingDown("lock_released")
	if !s.SteppingDown() {
		t.Error("after MarkSteppingDown, SteppingDown() must be true")
	}
	if got := rec.stepDowns["lock_released"]; got != 1 {
		t.Errorf("counter[lock_released] = %d, want 1", got)
	}

	// ClearSteppingDown closes the window without touching the counter (a
	// step-down is one event, not a Mark/Clear pair).
	s.ClearSteppingDown()
	if s.SteppingDown() {
		t.Error("after ClearSteppingDown, SteppingDown() must be false")
	}
	if got := rec.stepDowns["lock_released"]; got != 1 {
		t.Errorf("counter[lock_released] = %d after Clear, want still 1 (Clear is not an event)", got)
	}

	// A second step-down with a different reason is a distinct labeled count.
	s.MarkSteppingDown("check_timeout")
	if got := rec.stepDowns["check_timeout"]; got != 1 {
		t.Errorf("counter[check_timeout] = %d, want 1", got)
	}
	if got := rec.stepDowns["lock_released"]; got != 1 {
		t.Errorf("counter[lock_released] = %d after second step-down, want still 1", got)
	}
}

// (fakeRecorder is declared in scheduler_test.go and tracks stepDowns by
// reason; this file reuses it.)

// TestRecordReacquireSinceObserves pins the re-acquire latency contract: a
// non-zero stepDownAt against a non-nil Recorder records EXACTLY ONE histogram
// sample (operators alert on this as the leader-churn SLI); a zero stepDownAt
// (first boot, no prior step-down) records nothing; a nil Recorder is a no-op
// (the scheduler must tolerate being constructed without one).
func TestRecordReacquireSinceObserves(t *testing.T) {
	t.Run("non-zero stepDownAt + Recorder → one sample", func(t *testing.T) {
		rec := &fakeRecorder{}
		s := NewScheduler(nil, slog.New(&recordingHandler{}), time.Second)
		s.SetRecorder(rec)
		stepDownAt := time.Now().Add(-50 * time.Millisecond)
		s.RecordReacquireSince(stepDownAt)
		if got := len(rec.reacquireSamples); got != 1 {
			t.Fatalf("expected 1 sample, got %d", got)
		}
		// The recorded duration is wall-clock since stepDownAt — at least the
		// time we waited above (no upper bound — test sched can stall).
		if s := rec.reacquireSamples[0]; s < 40*time.Millisecond {
			t.Errorf("sample = %v, expected >= 40ms (we slept 50ms before recording)", s)
		}
	})
	t.Run("zero stepDownAt → no sample (no churn happened)", func(t *testing.T) {
		rec := &fakeRecorder{}
		s := NewScheduler(nil, slog.New(&recordingHandler{}), time.Second)
		s.SetRecorder(rec)
		s.RecordReacquireSince(time.Time{}) // zero value = first boot
		if got := len(rec.reacquireSamples); got != 0 {
			t.Errorf("zero stepDownAt should record nothing, got %d sample(s)", got)
		}
	})
	t.Run("nil Recorder → no-op (no panic)", func(t *testing.T) {
		s := NewScheduler(nil, slog.New(&recordingHandler{}), time.Second)
		// No SetRecorder; the scheduler is constructed with recorder=nil.
		s.RecordReacquireSince(time.Now().Add(-1 * time.Second))
		// If we get here without a panic, the nil-guard works.
	})
}
