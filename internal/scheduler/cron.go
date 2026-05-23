package scheduler

import (
	"time"

	"github.com/robfig/cron/v3"
)

// nextScheduledRun returns the logical date of the next due run for a cron
// expression given the latest run's logical date (nil if the DAG never ran) and
// the current time. due is false when nothing is due yet or the expression is
// unparseable.
//
// A DAG that has never run fires its most recent slot at or before now — i.e. it
// starts scheduling from registration (catchup=False semantics), without
// backfilling older slots. A DAG that has run advances to the next slot after its
// last logical date once that slot has arrived.
func nextScheduledRun(expr string, last *time.Time, now time.Time) (logical time.Time, due bool) {
	schedule, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, false
	}
	if last == nil {
		return mostRecentSlot(schedule, now)
	}
	next := schedule.Next(*last)
	if next.After(now) {
		return time.Time{}, false
	}
	return next, true
}

// mostRecentSlot returns the latest activation at or before now, computed in O(1)
// from the schedule's period (the cron library only exposes Next, not Prev). due
// is false when no slot falls at or before now.
func mostRecentSlot(schedule cron.Schedule, now time.Time) (logical time.Time, due bool) {
	n1 := schedule.Next(now) // first slot strictly after now
	period := schedule.Next(n1).Sub(n1)
	if period <= 0 {
		return time.Time{}, false
	}
	// The first slot after (now - period) lands at or before now for regular
	// schedules; once it fires, last_logical seeds the exact cadence thereafter.
	prev := schedule.Next(now.Add(-period))
	if prev.After(now) {
		return time.Time{}, false
	}
	return prev, true
}
