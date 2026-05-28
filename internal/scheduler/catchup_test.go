package scheduler

import (
	"testing"
	"time"
)

// TestDueScheduledSlots covers the catchup decision (#129): given a schedule,
// the latest run's logical date, "now", a catchup flag, and a start_date floor,
// returns the list of logical dates the scheduler should create runs for in
// the current tick — capped at maxSlots so a multi-hour outage cannot stall
// the tick.
//
// The contract pins both Airflow-parity rules:
//   - catchup=true backfills every missed slot.
//   - catchup=false jumps straight to the latest slot ≤ now (single run).
//
// And two safety guards: the per-tick cap and the start_date floor.
func TestDueScheduledSlots(t *testing.T) {
	hourly := "@hourly"
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	hour := func(h int) *time.Time {
		t := time.Date(2026, 5, 28, h, 0, 0, 0, time.UTC)
		return &t
	}

	tests := []struct {
		name      string
		expr      string
		last      *time.Time
		startDate *time.Time
		catchup   bool
		maxSlots  int
		want      []time.Time
	}{
		{
			// 6 missed hourly slots → 6 runs created, in chronological order.
			name: "catchup=true backfills every missed slot",
			expr: hourly, last: hour(6), catchup: true, maxSlots: 100,
			want: []time.Time{
				time.Date(2026, 5, 28, 7, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 8, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 11, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
			},
		},
		{
			// Same gap, catchup=false → only the most recent slot. The 5
			// skipped runs are intentionally lost; the operator chose this.
			name: "catchup=false jumps to the latest missed slot only",
			expr: hourly, last: hour(6), catchup: false, maxSlots: 100,
			want: []time.Time{time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)},
		},
		{
			// Cap < gap → we create the cap and stop; the remaining slots are
			// picked up on the next tick. Order is chronological so progress
			// is monotonic.
			name: "catchup=true respects maxSlots cap",
			expr: hourly, last: hour(6), catchup: true, maxSlots: 3,
			want: []time.Time{
				time.Date(2026, 5, 28, 7, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 8, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC),
			},
		},
		{
			// No missed slot → empty (next slot is strictly after now).
			name: "not due",
			expr: hourly, last: hour(12), catchup: true, maxSlots: 100,
			want: nil,
		},
		{
			// First-ever sight for a DAG with catchup=true and a start_date 3 h ago:
			// backfill from start_date forward (Airflow semantics).
			name: "first-run catchup=true backfills from start_date",
			expr: hourly, last: nil, startDate: hour(9), catchup: true, maxSlots: 100,
			want: []time.Time{
				time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 11, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
			},
		},
		{
			// First-ever sight with catchup=false: only the most recent slot.
			name: "first-run catchup=false fires only the latest slot",
			expr: hourly, last: nil, startDate: hour(9), catchup: false, maxSlots: 100,
			want: []time.Time{time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)},
		},
		{
			// start_date floor: even with catchup=true, no run is created
			// before the DAG's official start_date — protects against
			// accidentally creating runs against unintended dates.
			name: "start_date floor blocks pre-start slots",
			expr: hourly, last: hour(6), startDate: hour(9), catchup: true, maxSlots: 100,
			want: []time.Time{
				time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 11, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
			},
		},
		{
			// Unparseable schedule yields no slots (and never panics).
			name: "unparseable schedule yields nothing",
			expr: "* not a cron *", last: hour(6), catchup: true, maxSlots: 100,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dueScheduledSlots(tc.expr, tc.last, tc.startDate, now, tc.catchup, tc.maxSlots)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got=%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if !got[i].Equal(tc.want[i]) {
					t.Errorf("[%d] = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
