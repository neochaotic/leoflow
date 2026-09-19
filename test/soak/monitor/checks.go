package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"time"
)

// violation is one invariant that did not hold, with enough evidence attached
// that a reader on Monday morning does not have to re-derive the state.
type violation struct {
	At       time.Time `json:"at"`
	ElapsedS float64   `json:"elapsed_s"`
	Check    string    `json:"check"`
	Detail   string    `json:"detail"`
	Value    float64   `json:"value"`
	Limit    float64   `json:"limit"`
	Label    string    `json:"label"`
}

// faultWindow is a fault the harness declared. The monitor reads these but does
// not trust them: a window says when the harness THOUGHT the fault ran, and the
// recovery checks are what test whether reality agreed.
type faultWindow struct {
	Kind  string    `json:"kind"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Note  string    `json:"note,omitempty"`
}

// checker evaluates every invariant against one sample.
//
// The design rule is that each check names a property, not a mechanism, and
// carries its own suppression rule for declared fault windows. Nothing here is
// relaxed to make a run pass: a check either holds unconditionally or it holds
// after a stated recovery budget.
type checker struct {
	o      options
	counts map[string]int
	// firstUnhealthy tracks the start of the current unhealthy streak so the
	// health check can express itself as a duration rather than a sample count.
	firstUnhealthy time.Time
	sawHealthy     bool
	// stopLatch makes the stop condition sticky: once tripped it stays tripped,
	// so a directory that shrinks between samples cannot un-stop the run.
	stopLatch string
}

func newChecker(o options) *checker {
	return &checker{o: o, counts: map[string]int{}}
}

// Totals returns the per-check violation counts accumulated so far.
func (c *checker) Totals() map[string]int { return c.counts }

// loadFaultWindows re-reads the declared fault file every sample. It is re-read
// rather than cached because the harness appends to it while the monitor runs.
func (c *checker) loadFaultWindows() []faultWindow {
	if c.o.faultsFile == "" {
		return nil
	}
	f, err := os.Open(c.o.faultsFile) //nolint:gosec // operator-supplied harness path
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // best-effort close of a read-only file
	var out []faultWindow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var w faultWindow
		if json.Unmarshal(sc.Bytes(), &w) == nil && !w.Start.IsZero() {
			out = append(out, w)
		}
	}
	return out
}

// inFaultWindow reports whether t falls inside a declared window, extended by
// the recovery budget. Checks that a fault legitimately disturbs are suppressed
// inside it; checks about recovery are not.
func (c *checker) inFaultWindow(t time.Time, ws []faultWindow) bool {
	for _, w := range ws {
		end := w.End
		if end.IsZero() {
			end = w.Start.Add(c.o.recoveryBudget)
		}
		if !t.Before(w.Start) && !t.After(end.Add(c.o.recoveryBudget)) {
			return true
		}
	}
	return false
}

// Check evaluates every invariant and returns the violations for this sample.
// The checks are split across three helpers by the rule that governs each group:
// correctness holds unconditionally, wedge and liveness hold outside a declared
// fault window, and cadence needs enough elapsed time to have an expectation.
func (c *checker) Check(s sample) []violation {
	var out []violation
	add := func(name, detail string, value, limit float64) {
		out = append(out, violation{
			At: s.At, ElapsedS: s.ElapsedS, Check: name, Detail: detail,
			Value: value, Limit: limit, Label: s.Label,
		})
		c.counts[name]++
	}
	c.checkCorrectness(s, add)
	c.checkWedges(s, add)
	c.checkLiveness(s, add)
	c.checkCadence(s, add)
	return out
}

// addFunc records one violation. Passed down so each check group appends into the
// same slice without any of them owning it.
type addFunc func(name, detail string, value, limit float64)

// checkCorrectness evaluates the invariants that need no timing at all: they are
// either true or the system is wrong, right now. Nothing suppresses them.
func (c *checker) checkCorrectness(s sample, add addFunc) {
	if s.TerminalRunsWithActiveTIs > 0 {
		add("terminal_run_with_active_task",
			fmt.Sprintf("%d task instances are still non-terminal on a run the control plane already called terminal; the dashboard is lying about what is running",
				s.TerminalRunsWithActiveTIs), float64(s.TerminalRunsWithActiveTIs), 0)
	}
	if s.OverRetriedTIs > 0 {
		add("retry_budget_exceeded",
			fmt.Sprintf("%d task instances have try_number > max_tries", s.OverRetriedTIs),
			float64(s.OverRetriedTIs), 0)
	}
	if s.SuccessInHistory > 0 {
		add("success_replayed",
			fmt.Sprintf("%d archived attempts are in state success; history is written when an attempt is reset for retry, so a successful attempt in there means a success was re-run (at-most-once violated)",
				s.SuccessInHistory), float64(s.SuccessInHistory), 0)
	}
	if s.UpstreamFailedSuccesses > 0 {
		add("upstream_failed_task_ran",
			fmt.Sprintf("%d instances of soak_flaky.never_runs reached success; its upstream always fails, so it must never execute",
				s.UpstreamFailedSuccesses), float64(s.UpstreamFailedSuccesses), 0)
	}
	if s.ImportErrors > 0 {
		add("import_error",
			fmt.Sprintf("%d DAGs are failing to import; a DAG that stopped compiling stops producing runs and the cadence check would only notice later",
				s.ImportErrors), float64(s.ImportErrors), 0)
	}
	if !math.IsNaN(s.StepDowns) && s.StepDowns > 0 {
		add("leader_churn",
			fmt.Sprintf("leoflow_scheduler_step_downs_total = %.0f in a single-process soak; nothing is contending for the lock, so a step-down means the leader lost it",
				s.StepDowns), s.StepDowns, 0)
	}
	if !math.IsNaN(s.Undispatchable) && s.Undispatchable > 0 {
		add("undispatchable_task",
			fmt.Sprintf("leoflow_tasks_undispatchable_total = %.0f; a task was queued with no executor able to launch it", s.Undispatchable),
			s.Undispatchable, 0)
	}
}

// checkWedges evaluates what "the scheduler did not wedge" means as a
// measurement. A declared fault legitimately disturbs these, so they are
// suppressed inside a window plus the recovery budget, and only there.
func (c *checker) checkWedges(s sample, add addFunc) {
	if !s.InFaultWindow {
		if s.OldestQueuedS > c.o.queuedWedge.Seconds() {
			add("queued_wedge",
				fmt.Sprintf("a task instance has been in `queued` for %.0fs; the dispatch-lost threshold is %.0fs, so at this age the control plane itself considers the dispatch lost",
					s.OldestQueuedS, c.o.queuedWedge.Seconds()), s.OldestQueuedS, c.o.queuedWedge.Seconds())
		}
		if s.OldestScheduledS > c.o.scheduledWedge.Seconds() {
			add("scheduled_wedge",
				fmt.Sprintf("a task instance has been in `scheduled` for %.0fs without being dispatched", s.OldestScheduledS),
				s.OldestScheduledS, c.o.scheduledWedge.Seconds())
		}
		if s.OldestRunningRun > c.o.runWedge.Seconds() {
			add("run_wedge",
				fmt.Sprintf("a dag run has been `running` for %.0fs; the longest DAG in the battery finishes in minutes", s.OldestRunningRun),
				s.OldestRunningRun, c.o.runWedge.Seconds())
		}
	}
}

// checkLiveness asserts that the scheduler is healthy outside a declared window
// extended by the recovery budget. A fault that outlives the window the harness
// declared for it lands here, which is exactly the "the incident we thought was
// over was not" case.
func (c *checker) checkLiveness(s sample, add addFunc) {
	healthy := s.SchedulerHealth == "healthy" || s.SchedulerHealth == "ready"
	if healthy {
		c.sawHealthy = true
		c.firstUnhealthy = time.Time{}
	} else if c.firstUnhealthy.IsZero() {
		c.firstUnhealthy = s.At
	}
	if !healthy && !s.InFaultWindow && c.sawHealthy {
		streak := s.At.Sub(c.firstUnhealthy).Seconds()
		if streak >= c.o.healthTolerate.Seconds() {
			add("scheduler_unhealthy",
				fmt.Sprintf("scheduler health is %q (source %s) for %.0fs with no declared fault window open",
					s.SchedulerHealth, s.HealthSource, streak), streak, c.o.healthTolerate.Seconds())
		}
	}
}

// checkCadence catches the wedge that heartbeats: a scheduler that looks healthy
// and has quietly stopped creating scheduled runs. Only evaluated once the soak
// has run long enough for the expectation to be meaningful.
func (c *checker) checkCadence(s sample, add addFunc) {
	if !s.InFaultWindow && s.WindowMin >= 2*minCadencePeriodMin {
		for dagID, periodMin := range cadenceExpectations {
			expected := s.WindowMin / float64(periodMin)
			floorRuns := math.Floor(expected * 0.5)
			if floorRuns < 1 {
				continue
			}
			got := float64(s.RunsCreated[dagID])
			if got < floorRuns {
				add("cadence_starved",
					fmt.Sprintf("%s created %.0f runs in the last %.0f min; its schedule is every %d min, so at least %.0f were due",
						dagID, got, s.WindowMin, periodMin, floorRuns), got, floorRuns)
			}
		}
	}
}

// cadenceExpectations maps each soak DAG to its cron period in minutes. It is a
// literal rather than something read out of the database on purpose: the check
// exists to catch a scheduler that stopped honoring a schedule, and deriving the
// expectation from the same system under test would let it agree with itself.
var cadenceExpectations = map[string]int{
	"soak_ingest":    5,
	"soak_fanout":    5,
	"soak_chain":     3,
	"soak_operators": 5,
	"soak_flaky":     7,
	"soak_long":      15,
}

// minCadencePeriodMin is the shortest schedule in the battery; the cadence check
// waits for two of those before it says anything.
const minCadencePeriodMin = 3

// stopCondition returns a non-empty reason when the soak must stop cleanly. A
// stop is not a failure: it is the harness refusing to keep consuming a resource
// it promised to bound, and it happens while the evidence is still writable.
func (c *checker) stopCondition(s sample) string {
	if c.stopLatch != "" {
		return c.stopLatch
	}
	switch {
	case c.o.maxDBBytes > 0 && s.DBBytes > c.o.maxDBBytes:
		c.stopLatch = fmt.Sprintf("db_budget_exceeded:%d>%d", s.DBBytes, c.o.maxDBBytes)
	case c.o.maxDataBytes > 0 && s.DataBytes > c.o.maxDataBytes:
		c.stopLatch = fmt.Sprintf("data_budget_exceeded:%d>%d", s.DataBytes, c.o.maxDataBytes)
	case c.o.minFreeBytes > 0 && s.FreeBytes >= 0 && s.FreeBytes < c.o.minFreeBytes:
		c.stopLatch = fmt.Sprintf("disk_free_floor:%d<%d", s.FreeBytes, c.o.minFreeBytes)
	}
	return c.stopLatch
}
