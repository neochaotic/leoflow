package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// defaultOptions returns the thresholds the harness ships with, so a test that
// changes behavior by moving a threshold has to say so out loud.
func defaultOptions() options {
	return options{
		queuedWedge:    180 * time.Second,
		scheduledWedge: 300 * time.Second,
		runWedge:       30 * time.Minute,
		recoveryBudget: 2 * time.Minute,
		healthTolerate: 0,
		maxDBBytes:     4 << 30,
		maxDataBytes:   4 << 30,
		minFreeBytes:   10 << 30,
	}
}

// healthySample is a sample in which nothing is wrong. Each test perturbs
// exactly one field, so a failure names the invariant rather than the fixture.
func healthySample() sample {
	return sample{
		At:              time.Now(),
		SchedulerHealth: "healthy",
		HealthSource:    "monitor_health",
		StepDowns:       0,
		Undispatchable:  0,
		DBBytes:         10 << 20,
		DataBytes:       10 << 20,
		FreeBytes:       100 << 30,
		RunsCreated:     map[string]int{},
	}
}

func checkNames(vs []violation) map[string]bool {
	out := map[string]bool{}
	for _, v := range vs {
		out[v.Check] = true
	}
	return out
}

// TestHealthySampleProducesNoViolations is the floor under every other test
// here: if the neutral fixture is already red, nothing below proves anything.
func TestHealthySampleProducesNoViolations(t *testing.T) {
	c := newChecker(defaultOptions())
	if vs := c.Check(healthySample()); len(vs) != 0 {
		t.Fatalf("healthy sample produced %d violations: %v", len(vs), checkNames(vs))
	}
}

// TestCorrectnessInvariantsFireEvenInsideAFaultWindow pins the rule that
// separates the two families of check: a wedge is something a fault can cause,
// while an impossible database state is never excusable. A fault window must not
// be a way to launder one into the other.
func TestCorrectnessInvariantsFireEvenInsideAFaultWindow(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*sample)
		check string
	}{
		{"active task on a terminal run", func(s *sample) { s.TerminalRunsWithActiveTIs = 1 }, "terminal_run_with_active_task"},
		{"try number past max tries", func(s *sample) { s.OverRetriedTIs = 1 }, "retry_budget_exceeded"},
		{"success archived into history", func(s *sample) { s.SuccessInHistory = 1 }, "success_replayed"},
		{"upstream-failed task ran", func(s *sample) { s.UpstreamFailedExecuted = 1 }, "upstream_failed_task_ran"},
		{"dag import error", func(s *sample) { s.ImportErrors = 1 }, "import_error"},
		{"leader stepped down", func(s *sample) { s.StepDowns = 1 }, "leader_churn"},
		{"task had no executor", func(s *sample) { s.Undispatchable = 1 }, "undispatchable_task"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newChecker(defaultOptions())
			s := healthySample()
			s.InFaultWindow = true // the suppression that must NOT apply here
			tc.mut(&s)
			got := checkNames(c.Check(s))
			if !got[tc.check] {
				t.Fatalf("expected %q inside a fault window, got %v", tc.check, got)
			}
		})
	}
}

// TestWedgeChecksFireOutsideAWindowAndAreSuppressedInside pins the other half of
// that rule: a declared fault legitimately disturbs these, and only these.
func TestWedgeChecksFireOutsideAWindowAndAreSuppressedInside(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*sample)
		check string
	}{
		{"queued past the dispatch-lost threshold", func(s *sample) { s.OldestQueuedS = 181 }, "queued_wedge"},
		{"scheduled past its threshold", func(s *sample) { s.OldestScheduledS = 301 }, "scheduled_wedge"},
		{"run running past its threshold", func(s *sample) { s.OldestRunningRun = 1801 }, "run_wedge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outside := newChecker(defaultOptions())
			s := healthySample()
			tc.mut(&s)
			if got := checkNames(outside.Check(s)); !got[tc.check] {
				t.Fatalf("expected %q outside a fault window, got %v", tc.check, got)
			}

			inside := newChecker(defaultOptions())
			s2 := healthySample()
			tc.mut(&s2)
			s2.InFaultWindow = true
			if got := checkNames(inside.Check(s2)); got[tc.check] {
				t.Fatalf("%q fired inside a declared fault window; a declared fault is allowed to cause it", tc.check)
			}
		})
	}
}

// TestWedgeChecksDoNotFireExactlyAtTheThreshold guards the off-by-one that would
// make every soak red at the boundary: the threshold is the largest acceptable
// age, not the smallest unacceptable one.
func TestWedgeChecksDoNotFireExactlyAtTheThreshold(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.OldestQueuedS = 180
	s.OldestScheduledS = 300
	s.OldestRunningRun = 1800
	if vs := c.Check(s); len(vs) != 0 {
		t.Fatalf("a sample exactly at every threshold produced %v", checkNames(vs))
	}
}

// TestSchedulerUnhealthyNeedsAPriorHealthySample stops the harness from
// reporting a violation for the window between its own start and the control
// plane's first healthy answer, which is a harness artifact and not a defect.
func TestSchedulerUnhealthyNeedsAPriorHealthySample(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.SchedulerHealth = "unreachable"
	if got := checkNames(c.Check(s)); got["scheduler_unhealthy"] {
		t.Fatal("reported unhealthy before ever seeing the control plane healthy")
	}
	// Once healthy has been observed, a later unhealthy sample is real.
	if vs := c.Check(healthySample()); len(vs) != 0 {
		t.Fatalf("healthy sample produced %v", checkNames(vs))
	}
	s2 := healthySample()
	s2.SchedulerHealth = "unreachable"
	if got := checkNames(c.Check(s2)); !got["scheduler_unhealthy"] {
		t.Fatalf("expected scheduler_unhealthy after a healthy sample, got %v", got)
	}
}

// TestReadyzCountsAsHealthy pins the degraded liveness source: when
// /api/v2/monitor/health cannot be read, /readyz answering 200 is reported as
// "ready", and that must not itself be a violation.
func TestReadyzCountsAsHealthy(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.SchedulerHealth, s.HealthSource = "ready", "readyz"
	if vs := c.Check(s); len(vs) != 0 {
		t.Fatalf("readyz-sourced health produced %v", checkNames(vs))
	}
}

// TestCadenceStarvedIsTheWedgeThatHeartbeats is the check no other signal
// covers: a scheduler that answers healthy and has stopped creating runs.
func TestCadenceStarvedIsTheWedgeThatHeartbeats(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.WindowMin = 18 // soak_chain runs every 3 minutes, so 6 were due
	s.RunsCreated = map[string]int{
		"soak_ingest": 4, "soak_fanout": 4, "soak_operators": 4,
		"soak_flaky": 3, "soak_long": 1,
		// soak_chain deliberately absent
	}
	got := checkNames(c.Check(s))
	if !got["cadence_starved"] {
		t.Fatalf("expected cadence_starved for soak_chain, got %v", got)
	}

	// The same window with the runs present must be quiet.
	c2 := newChecker(defaultOptions())
	s2 := healthySample()
	s2.WindowMin = 18
	s2.RunsCreated = map[string]int{
		"soak_chain": 6, "soak_ingest": 4, "soak_fanout": 4,
		"soak_operators": 4, "soak_flaky": 3, "soak_long": 1,
	}
	if vs := c2.Check(s2); len(vs) != 0 {
		t.Fatalf("a window meeting every cadence produced %v", checkNames(vs))
	}
}

// TestCadenceIsSilentBeforeTheWindowIsMeaningful stops the check from firing in
// the first minutes, when no run can have been due yet.
func TestCadenceIsSilentBeforeTheWindowIsMeaningful(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.WindowMin = 5 // below 2 x the shortest schedule in the battery
	if got := checkNames(c.Check(s)); got["cadence_starved"] {
		t.Fatal("cadence_starved fired before any run could have been due")
	}
}

// TestCountersAreNotAssertedWhenTheScrapeFailed pins that an unreachable
// /metrics endpoint (NaN) is not silently read as zero, nor as a violation. The
// health check is what reports an unreachable control plane.
func TestCountersAreNotAssertedWhenTheScrapeFailed(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.StepDowns, s.Undispatchable, s.DispatchAtCap = nanMetric(), nanMetric(), nanMetric()
	if vs := c.Check(s); len(vs) != 0 {
		t.Fatalf("a failed metrics scrape produced %v", checkNames(vs))
	}
}

// TestInFaultWindowExtendsByTheRecoveryBudget pins the suppression geometry the
// self-test depends on: the window closes one recovery budget after the harness
// said the fault ended, and a fault that outlives its declared window is caught.
func TestInFaultWindowExtendsByTheRecoveryBudget(t *testing.T) {
	c := newChecker(defaultOptions())
	start := time.Now()
	ws := []faultWindow{{Kind: "pgpause", Start: start, End: start.Add(45 * time.Second)}}

	for _, tc := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before the fault", start.Add(-time.Second), false},
		{"during the fault", start.Add(20 * time.Second), true},
		{"inside the recovery budget", start.Add(45*time.Second + 90*time.Second), true},
		{"one second past the budget", start.Add(45*time.Second + 2*time.Minute + time.Second), false},
	} {
		if got := c.inFaultWindow(tc.at, ws); got != tc.want {
			t.Errorf("%s: inFaultWindow = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestStopConditionsAreBudgetsNotFailures pins that exceeding a budget stops the
// run and latches, and that the latch cannot be undone by a later sample: a
// directory that shrinks between samples must not un-stop a soak.
func TestStopConditionsAreBudgetsNotFailures(t *testing.T) {
	o := defaultOptions()
	o.maxDBBytes = 1000
	c := newChecker(o)

	s := healthySample()
	s.DBBytes = 999
	if r := c.stopCondition(s); r != "" {
		t.Fatalf("stopped under budget: %q", r)
	}
	s.DBBytes = 1001
	first := c.stopCondition(s)
	if first == "" {
		t.Fatal("did not stop when the database budget was exceeded")
	}
	s.DBBytes = 10
	if again := c.stopCondition(s); again != first {
		t.Fatalf("stop latch was released: %q then %q", first, again)
	}
}

// TestDiskFreeFloorStopsTheRun pins the guard that matters most on a laptop:
// filling the machine's disk is a worse outcome than filling the soak's budget.
func TestDiskFreeFloorStopsTheRun(t *testing.T) {
	o := defaultOptions()
	o.minFreeBytes = 10 << 30
	c := newChecker(o)
	s := healthySample()
	s.FreeBytes = 1 << 30
	if r := c.stopCondition(s); r == "" {
		t.Fatal("did not stop when free space dropped below the floor")
	}

	// An unknown free-space reading (-1) must not be read as "zero free".
	c2 := newChecker(o)
	s2 := healthySample()
	s2.FreeBytes = -1
	if r := c2.stopCondition(s2); r != "" {
		t.Fatalf("stopped on an unknown free-space reading: %q", r)
	}
}

// TestViolationCarriesItsEvidence pins that a violation is readable on Monday
// morning without re-deriving the state: it names the check, the value, the
// limit and says what went wrong in words.
func TestViolationCarriesItsEvidence(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.OldestQueuedS = 240
	vs := c.Check(s)
	if len(vs) != 1 {
		t.Fatalf("expected exactly one violation, got %v", checkNames(vs))
	}
	v := vs[0]
	if v.Value != 240 || v.Limit != 180 {
		t.Errorf("value/limit = %v/%v, want 240/180", v.Value, v.Limit)
	}
	if v.Detail == "" || v.At.IsZero() {
		t.Errorf("violation is missing evidence: detail=%q at=%v", v.Detail, v.At)
	}
	if c.Totals()["queued_wedge"] != 1 {
		t.Errorf("violation was not counted: %v", c.Totals())
	}
}

// TestCadenceExpectationsCoverEveryShippedDag stops the battery from silently
// losing its cadence coverage when a DAG is added to test/soak/dags without a
// matching expectation, which would leave that DAG's schedule unasserted.
func TestCadenceExpectationsCoverEveryShippedDag(t *testing.T) {
	shipped := []string{"soak_ingest", "soak_fanout", "soak_chain", "soak_operators", "soak_flaky", "soak_long"}
	for _, dag := range shipped {
		if _, ok := cadenceExpectations[dag]; !ok {
			t.Errorf("%s ships in test/soak/dags but has no cadence expectation", dag)
		}
	}
	if len(cadenceExpectations) != len(shipped) {
		t.Errorf("cadenceExpectations has %d entries for %d shipped DAGs", len(cadenceExpectations), len(shipped))
	}
	for dag, period := range cadenceExpectations {
		if period < minCadencePeriodMin {
			t.Errorf("%s declares a %d-minute period, shorter than minCadencePeriodMin (%d), so the cadence check would wait too little",
				dag, period, minCadencePeriodMin)
		}
	}
}

// TestNeverRunsInvariantKeysOnExecutionNotOnSuccess is a contract between two
// files that have to agree or the invariant is decoration: soak_flaky.never_runs
// raises on entry, so it can NEVER reach `success`, and a query that looks for a
// successful never_runs is a query whose answer is always zero. The invariant is
// "it executed", and the durable evidence of execution is started_at.
func TestNeverRunsInvariantKeysOnExecutionNotOnSuccess(t *testing.T) {
	dag, err := os.ReadFile(filepath.Join("..", "dags", "soak_flaky", "dag.py"))
	if err != nil {
		t.Fatalf("reading the soak_flaky DAG: %v", err)
	}
	body := string(dag)
	if !strings.Contains(body, "def never_runs(") {
		t.Fatal("soak_flaky no longer defines never_runs; the invariant has no subject")
	}
	// The task raises, so `state = 'success'` is unreachable for it by construction.
	if !strings.Contains(body, "raise AssertionError") {
		t.Fatal("never_runs no longer raises; re-derive which states the query may key on")
	}
	if !strings.Contains(correctnessQuery, "task_id = 'never_runs' AND started_at IS NOT NULL") {
		t.Errorf("the never_runs invariant does not key on started_at; a task that raises can only be caught by evidence that it ran:\n%s", correctnessQuery)
	}
	if strings.Contains(correctnessQuery, "task_id = 'never_runs' AND state = 'success'") {
		t.Error("the never_runs invariant keys on success, which never_runs cannot reach: the check can never fire")
	}
}

// TestCadenceExpectationsMatchTheShippedSchedules closes the gap the literal
// table opens on purpose. Deriving the expectation from the database would let
// the system under test agree with itself, so the table is hand-written; the
// cost of that choice is that a DAG whose cron changes leaves the table wrong,
// and a wrong table is either a false red or, worse, a cadence check that a
// slower schedule can never trip. This asserts the two agree at build time,
// which is the one place it can be done without asking the scheduler.
func TestCadenceExpectationsMatchTheShippedSchedules(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "dags"))
	if err != nil {
		t.Fatalf("reading test/soak/dags: %v", err)
	}
	re := regexp.MustCompile(`schedule\s*=\s*"\*/(\d+) \* \* \* \*"`)
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		src, rerr := os.ReadFile(filepath.Join("..", "dags", e.Name(), "dag.py"))
		if rerr != nil {
			t.Errorf("%s has no dag.py: %v", e.Name(), rerr)
			continue
		}
		m := re.FindSubmatch(src)
		if m == nil {
			t.Errorf("%s does not declare a `*/N * * * *` schedule the cadence check can express", e.Name())
			continue
		}
		period, cerr := strconv.Atoi(string(m[1]))
		if cerr != nil {
			t.Fatalf("%s: unparsable period %q", e.Name(), m[1])
		}
		seen[e.Name()] = true
		want, ok := cadenceExpectations[e.Name()]
		if !ok {
			t.Errorf("%s ships in test/soak/dags but has no cadence expectation", e.Name())
			continue
		}
		if want != period {
			t.Errorf("%s runs every %d min but cadenceExpectations says %d", e.Name(), period, want)
		}
	}
	for dag := range cadenceExpectations {
		if !seen[dag] {
			t.Errorf("cadenceExpectations names %s, which no longer ships in test/soak/dags", dag)
		}
	}
}

// TestStopConditionCountsEveryDatabaseInTheCluster pins the budget against what
// is actually on the disk. `pg_database_size(current_database())` sees the
// metadatabase only, while the operator leg writes into a second database
// (soak_warehouse) in the same container and on the same volume. A cap that
// measures one of the two is not the cap the README promises.
func TestStopConditionCountsEveryDatabaseInTheCluster(t *testing.T) {
	o := defaultOptions()
	o.maxDBBytes = 1000
	c := newChecker(o)
	s := healthySample()
	s.DBBytes = 400          // the metadatabase alone is under budget
	s.DBClusterBytes = 1_400 // every database on the volume is not
	if r := c.stopCondition(s); r == "" {
		t.Fatal("did not stop when the cluster exceeded the database budget")
	}
}

// TestStopConditionFallsBackToTheMetadatabase keeps the cap working when the
// cluster-wide reading is unavailable (an unprivileged role, an old server):
// an unknown total must never be read as zero.
func TestStopConditionFallsBackToTheMetadatabase(t *testing.T) {
	o := defaultOptions()
	o.maxDBBytes = 1000
	c := newChecker(o)
	s := healthySample()
	s.DBBytes = 1_400
	s.DBClusterBytes = 0
	if r := c.stopCondition(s); r == "" {
		t.Fatal("did not stop on the metadatabase reading when the cluster reading was missing")
	}
}

// TestEveryEarlyStopIsReportedAsAnEarlyStop pins the one thing an unattended
// operator reads on Monday: whether the run they asked for actually ran. All
// three stop conditions end the soak before its ceiling, so all three must be
// annotated the same way and must not be reported as a completed clean run.
func TestEveryEarlyStopIsReportedAsAnEarlyStop(t *testing.T) {
	for _, reason := range []string{"db_budget_exceeded:2>1", "data_budget_exceeded:2>1", "disk_free_floor:1<2"} {
		if !isEarlyStop(reason) {
			t.Errorf("%q is a stop condition but is not reported as an early stop", reason)
		}
	}
	for _, reason := range []string{"duration_reached", "signal"} {
		if isEarlyStop(reason) {
			t.Errorf("%q is not a budget stop but was reported as one", reason)
		}
	}
}

// TestShapeTableCarriesTheBaselineProbe pins the control that makes the headline
// reading interpretable. The tick probe is timed from another process on a
// laptop that is also doing other things, so a bucket where the probe rose is
// ambiguous between "ActiveRuns got more expensive" and "everything did". A
// trivial round trip taken in the same sample separates the two, and it has to
// reach the table a reader actually looks at.
func TestShapeTableCarriesTheBaselineProbe(t *testing.T) {
	r := &reporter{o: defaultOptions()}
	for i := 0; i < 8; i++ {
		r.trend = append(r.trend, trendPoint{
			elapsedS: float64(i * 10), tickProbe: 2.0, baseline: 0.4,
			totalRuns: int64(i), activeRuns: 1,
		})
	}
	md := r.renderShape()
	if !strings.Contains(md, "baseline") {
		t.Errorf("the shape table has no baseline column, so a reader cannot tell a slower ActiveRuns from a slower machine:\n%s", md)
	}
}
