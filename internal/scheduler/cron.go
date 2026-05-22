package scheduler

import (
	"time"

	"github.com/robfig/cron/v3"
)

// nextScheduledRun returns the logical date of the next due run for a cron
// expression given the latest run's logical date (nil if the DAG never ran) and
// the current time. due is false when nothing is due yet or the expression is
// unparseable. A DAG that has never run waits for its next slot (no backfill in
// the MVP); a DAG behind schedule catches up one slot per tick.
func nextScheduledRun(expr string, last *time.Time, now time.Time) (logical time.Time, due bool) {
	schedule, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, false
	}
	base := now
	if last != nil {
		base = *last
	}
	next := schedule.Next(base)
	if next.After(now) {
		return time.Time{}, false
	}
	return next, true
}
