package scheduler

import (
	"testing"
	"time"
)

func TestNextScheduledRun(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 30, 0, 0, time.UTC)

	last := time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC)
	got, due := nextScheduledRun("@hourly", &last, now)
	if !due {
		t.Fatal("a slot at 12:00 should be due at 12:30")
	}
	if !got.Equal(time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("logical = %v, want 12:00", got)
	}

	recent := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	if _, due := nextScheduledRun("@hourly", &recent, now); due {
		t.Error("next slot (13:00) is in the future; should not be due")
	}

	if _, due := nextScheduledRun("@hourly", nil, now); due {
		t.Error("a DAG that never ran should wait for its next slot, not backfill")
	}

	if _, due := nextScheduledRun("not a cron expression", &last, now); due {
		t.Error("an unparseable expression should not be due")
	}
}
