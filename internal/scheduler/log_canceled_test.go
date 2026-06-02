package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
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
		name    string
		err     error
		wantLvl slog.Level
	}{
		{
			name:    "context.Canceled itself → WARN",
			err:     context.Canceled,
			wantLvl: slog.LevelWarn,
		},
		{
			name:    "fmt.Errorf wrap of context.Canceled → WARN (errors.Is unwraps)",
			err:     fmt.Errorf("listing orphan candidates: %w", context.Canceled),
			wantLvl: slog.LevelWarn,
		},
		{
			name:    "DeadlineExceeded → ERROR (a real stall, not a step-down)",
			err:     context.DeadlineExceeded,
			wantLvl: slog.LevelError,
		},
		{
			name:    "generic infra error → ERROR",
			err:     errors.New("postgres connection refused"),
			wantLvl: slog.LevelError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &recordingHandler{}
			logSchedulerError(slog.New(h), "orphan reaper", tc.err)
			if len(h.records) != 1 {
				t.Fatalf("expected 1 record, got %d", len(h.records))
			}
			if got := h.records[0].Level; got != tc.wantLvl {
				t.Errorf("level = %v, want %v (err=%v)", got, tc.wantLvl, tc.err)
			}
			if !strings.Contains(h.records[0].Message, "orphan reaper") {
				t.Errorf("message %q lost the caller's prefix", h.records[0].Message)
			}
		})
	}
}
