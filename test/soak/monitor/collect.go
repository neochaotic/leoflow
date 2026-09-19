package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/storage"
)

// sample is one observation of the whole system. Every field is written to
// samples.jsonl verbatim; the JSON keys are the stable interface that a later
// analysis script reads, so they are named for what they mean and not for the
// query that produced them.
type sample struct {
	At        time.Time `json:"at"`
	ElapsedS  float64   `json:"elapsed_s"`
	Label     string    `json:"label"`
	Seq       int       `json:"seq"`
	StopAdvis string    `json:"stop_advisory,omitempty"`

	// Liveness of the control plane.
	SchedulerHealth string `json:"scheduler_health"`
	HealthSource    string `json:"health_source"`
	APIReachable    bool   `json:"api_reachable"`

	// The active set: what the scheduler has to reason about right now.
	ActiveRuns  int `json:"active_runs"`
	QueuedTIs   int `json:"queued_tis"`
	RunningTIs  int `json:"running_tis"`
	ScheduledTI int `json:"scheduled_tis"`
	RetryTIs    int `json:"up_for_retry_tis"`
	ReschedTIs  int `json:"up_for_reschedule_tis"`

	// Age of the oldest thing that has not moved. These are the wedge signals.
	OldestQueuedS    float64 `json:"oldest_queued_s"`
	OldestScheduledS float64 `json:"oldest_scheduled_s"`
	OldestRunningRun float64 `json:"oldest_running_run_s"`

	// The historical set: what the database has accumulated. The pair
	// (ActiveRuns, TotalRuns) is what makes the tick-cost curve interpretable.
	TotalRuns    int64 `json:"total_runs"`
	TotalTIs     int64 `json:"total_task_instances"`
	TotalHistory int64 `json:"total_ti_history"`
	TotalXCom    int64 `json:"total_xcom"`
	ImportErrors int64 `json:"import_errors"`
	// The two tables nobody budgets for until a weekend run fills the disk with
	// them. Counted so the growth model in the README is measured, not guessed.
	TotalAudit        int64 `json:"total_audit_log"`
	TotalStateHistory int64 `json:"total_task_state_history"`

	// The tick-cost probe: the wall time of SchedulerStore.ActiveRuns, which is
	// a scheduler tick's dominant read. See the README for why this stands in
	// for a metric the product does not emit.
	TickProbeMS   float64 `json:"tick_probe_ms"`
	TickProbeRuns int     `json:"tick_probe_runs"`
	TickProbeErr  string  `json:"tick_probe_error,omitempty"`

	// Dispatch latency (queued -> running) over the rolling window, split by the
	// task's operator so the native and non-native paths can be compared.
	DispatchN      int                `json:"dispatch_n"`
	DispatchP50    float64            `json:"dispatch_p50_ms"`
	DispatchP95    float64            `json:"dispatch_p95_ms"`
	DispatchP99    float64            `json:"dispatch_p99_ms"`
	DispatchMax    float64            `json:"dispatch_max_ms"`
	DispatchByType map[string]latency `json:"dispatch_by_operator"`

	// Cadence: runs created per DAG in the rolling window, the signal that
	// catches a scheduler that heartbeats but has stopped creating work.
	RunsCreated map[string]int `json:"runs_created_in_window"`
	WindowMin   float64        `json:"cadence_window_min"`

	// Correctness invariants evaluated in SQL.
	TerminalRunsWithActiveTIs int64 `json:"terminal_runs_with_active_tis"`
	OverRetriedTIs            int64 `json:"over_retried_tis"`
	SuccessInHistory          int64 `json:"success_rows_in_history"`
	UpstreamFailedSuccesses   int64 `json:"never_runs_succeeded"`

	// Product counters scraped from /metrics.
	StepDowns      float64 `json:"scheduler_step_downs_total"`
	Undispatchable float64 `json:"tasks_undispatchable_total"`
	DispatchAtCap  float64 `json:"dispatch_at_capacity_total"`

	// Budget.
	DBBytes   int64 `json:"db_bytes"`
	DataBytes int64 `json:"data_bytes"`
	FreeBytes int64 `json:"free_bytes"`

	// Outcome accounting over the whole soak so far.
	RunsSuccess int64 `json:"runs_success_total"`
	RunsFailed  int64 `json:"runs_failed_total"`

	InFaultWindow bool   `json:"in_fault_window"`
	Violations    int    `json:"violations"`
	SampleError   string `json:"sample_error,omitempty"`
}

// latency is one percentile summary, in milliseconds.
type latency struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	Max float64 `json:"max_ms"`
}

// collector owns the connections and produces one sample per call.
type collector struct {
	o     options
	pool  *pgxpool.Pool
	pg    *storage.Postgres
	store *storage.SchedulerStore
	http  *http.Client
	seq   int
}

func newCollector(ctx context.Context, o options) (*collector, error) {
	pool, err := pgxpool.New(ctx, o.dbURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to the soak database: %w", err)
	}
	if perr := pool.Ping(ctx); perr != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging the soak database (is it up and migrated?): %w", perr)
	}
	// A second, product-owned handle: the tick probe must go through the real
	// SchedulerStore, not a hand-written copy of its query, or it measures
	// something the scheduler does not do.
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: o.dbURL})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("opening the product storage handle: %w", err)
	}
	return &collector{
		o:     o,
		pool:  pool,
		pg:    pg,
		store: storage.NewSchedulerStore(pg),
		http:  &http.Client{Timeout: 5 * time.Second},
	}, nil
}

// Close releases both database handles.
func (c *collector) Close() {
	if c.pg != nil {
		c.pg.Close()
	}
	if c.pool != nil {
		c.pool.Close()
	}
}

// Sample takes one observation. It returns a partially filled sample together
// with the first error it hit, because a half-observed system during an outage
// is exactly the evidence the soak exists to keep.
func (c *collector) Sample(ctx context.Context, started time.Time) (sample, error) {
	c.seq++
	now := time.Now()
	s := sample{
		At:          now,
		ElapsedS:    now.Sub(started).Seconds(),
		Label:       c.o.label,
		Seq:         c.seq,
		RunsCreated: map[string]int{},
	}

	// Liveness first: it is the one reading that is still meaningful when
	// everything below fails.
	s.SchedulerHealth, s.HealthSource, s.APIReachable = c.health(ctx)
	s.StepDowns, s.Undispatchable, s.DispatchAtCap = c.counters(ctx)
	s.FreeBytes = freeBytes(c.o.outDir)
	if c.o.dataDir != "" {
		s.DataBytes = dirBytes(c.o.dataDir)
	}

	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	keep(c.states(qctx, &s))
	keep(c.totals(qctx, &s))
	keep(c.correctness(qctx, &s))
	keep(c.dispatchLatency(qctx, &s))
	keep(c.cadence(qctx, &s, started))
	keep(c.dbSize(qctx, &s))

	// The probe goes last so a slow probe does not distort the gauges above.
	t0 := time.Now()
	runs, perr := c.store.ActiveRuns(qctx)
	s.TickProbeMS = float64(time.Since(t0)) / float64(time.Millisecond)
	if perr != nil {
		s.TickProbeErr = perr.Error()
		keep(nil) // a probe error is recorded, not escalated: health covers it
	} else {
		s.TickProbeRuns = len(runs)
	}
	return s, firstErr
}

func (c *collector) states(ctx context.Context, s *sample) error {
	const q = `
SELECT
  count(*) FILTER (WHERE state = 'queued')             AS queued,
  count(*) FILTER (WHERE state = 'running')            AS running,
  count(*) FILTER (WHERE state = 'scheduled')          AS scheduled,
  count(*) FILTER (WHERE state = 'up_for_retry')       AS retry,
  count(*) FILTER (WHERE state = 'up_for_reschedule')  AS resched,
  COALESCE(EXTRACT(EPOCH FROM (now() - min(queued_at) FILTER (WHERE state = 'queued'))), 0)       AS oldest_queued,
  COALESCE(EXTRACT(EPOCH FROM (now() - min(scheduled_at) FILTER (WHERE state = 'scheduled'))), 0) AS oldest_scheduled
FROM task_instances`
	if err := c.pool.QueryRow(ctx, q).Scan(
		&s.QueuedTIs, &s.RunningTIs, &s.ScheduledTI, &s.RetryTIs, &s.ReschedTIs,
		&s.OldestQueuedS, &s.OldestScheduledS,
	); err != nil {
		return fmt.Errorf("task-instance states: %w", err)
	}
	const rq = `
SELECT count(*) FILTER (WHERE state IN ('queued','running')),
       COALESCE(EXTRACT(EPOCH FROM (now() - min(queued_at) FILTER (WHERE state = 'running'))), 0)
FROM dag_runs`
	if err := c.pool.QueryRow(ctx, rq).Scan(&s.ActiveRuns, &s.OldestRunningRun); err != nil {
		return fmt.Errorf("dag-run states: %w", err)
	}
	return nil
}

func (c *collector) totals(ctx context.Context, s *sample) error {
	const q = `
SELECT (SELECT count(*) FROM dag_runs),
       (SELECT count(*) FROM task_instances),
       (SELECT count(*) FROM task_instance_history),
       (SELECT count(*) FROM xcom_store),
       (SELECT count(*) FROM import_errors),
       (SELECT count(*) FROM dag_runs WHERE state = 'success'),
       (SELECT count(*) FROM dag_runs WHERE state = 'failed'),
       (SELECT count(*) FROM audit_log),
       (SELECT count(*) FROM task_state_history)`
	if err := c.pool.QueryRow(ctx, q).Scan(
		&s.TotalRuns, &s.TotalTIs, &s.TotalHistory, &s.TotalXCom, &s.ImportErrors,
		&s.RunsSuccess, &s.RunsFailed, &s.TotalAudit, &s.TotalStateHistory,
	); err != nil {
		return fmt.Errorf("totals: %w", err)
	}
	return nil
}

// correctness evaluates the invariants that need no timing at all: they are
// either true or the system is wrong, right now.
func (c *collector) correctness(ctx context.Context, s *sample) error {
	const q = `
SELECT
  (SELECT count(*) FROM task_instances ti
     JOIN dag_runs r ON r.id = ti.dag_run_id
    WHERE r.state IN ('success','failed')
      AND ti.state IN ('none','scheduled','queued','running','up_for_retry','up_for_reschedule')),
  (SELECT count(*) FROM task_instances WHERE try_number > max_tries),
  (SELECT count(*) FROM task_instance_history WHERE state = 'success'),
  (SELECT count(*) FROM task_instances WHERE task_id = 'never_runs' AND state = 'success')`
	if err := c.pool.QueryRow(ctx, q).Scan(
		&s.TerminalRunsWithActiveTIs, &s.OverRetriedTIs, &s.SuccessInHistory, &s.UpstreamFailedSuccesses,
	); err != nil {
		return fmt.Errorf("correctness invariants: %w", err)
	}
	return nil
}

func (c *collector) dispatchLatency(ctx context.Context, s *sample) error {
	const q = `
SELECT operator, EXTRACT(EPOCH FROM (started_at - queued_at)) * 1000
FROM task_instances
WHERE started_at IS NOT NULL AND queued_at IS NOT NULL
  AND started_at >= now() - $1::interval
  AND started_at >= queued_at`
	rows, err := c.pool.Query(ctx, q, fmt.Sprintf("%d seconds", int(c.o.latencyWindow.Seconds())))
	if err != nil {
		return fmt.Errorf("dispatch latency: %w", err)
	}
	defer rows.Close()

	all := []float64{}
	byType := map[string][]float64{}
	for rows.Next() {
		var op string
		var ms float64
		if err := rows.Scan(&op, &ms); err != nil {
			return fmt.Errorf("dispatch latency scan: %w", err)
		}
		all = append(all, ms)
		byType[op] = append(byType[op], ms)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("dispatch latency rows: %w", err)
	}

	s.DispatchN = len(all)
	s.DispatchP50, s.DispatchP95, s.DispatchP99, s.DispatchMax = pcts(all)
	s.DispatchByType = map[string]latency{}
	for op, v := range byType {
		p50, p95, _, mx := pcts(v)
		s.DispatchByType[op] = latency{N: len(v), P50: p50, P95: p95, Max: mx}
	}
	return nil
}

// cadence counts runs created per DAG inside a window that never exceeds the
// soak's own elapsed time, so the ratio is meaningful from the first minute.
func (c *collector) cadence(ctx context.Context, s *sample, started time.Time) error {
	win := time.Since(started)
	if win > time.Hour {
		win = time.Hour
	}
	s.WindowMin = win.Minutes()
	const q = `
SELECT d.dag_id, count(*)
FROM dag_runs r JOIN dags d ON d.id = r.dag_id
WHERE r.queued_at >= now() - $1::interval
GROUP BY d.dag_id`
	rows, err := c.pool.Query(ctx, q, fmt.Sprintf("%d seconds", int(win.Seconds())+1))
	if err != nil {
		return fmt.Errorf("cadence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return fmt.Errorf("cadence scan: %w", err)
		}
		s.RunsCreated[id] = n
	}
	return rows.Err()
}

func (c *collector) dbSize(ctx context.Context, s *sample) error {
	if err := c.pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&s.DBBytes); err != nil {
		return fmt.Errorf("database size: %w", err)
	}
	return nil
}

// health reads the scheduler's own view of itself, preferring /monitor/health
// (which reports the advisory-lock leader liveness) and degrading to /readyz.
// The source is recorded so a reader never mistakes one for the other.
func (c *collector) health(ctx context.Context) (status, source string, reachable bool) {
	base := strings.TrimRight(c.o.apiURL, "/")
	if st, ok := c.schedulerStatus(ctx, base); ok {
		return st, "monitor_health", true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/readyz", http.NoBody)
	if err != nil {
		return "unreachable", "readyz", false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "unreachable", "readyz", false
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close of a probe response
	if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
		return "unreachable", "readyz", false
	}
	if resp.StatusCode == http.StatusOK {
		return "ready", "readyz", true
	}
	return "unhealthy", "readyz", true
}

// schedulerStatus reads .scheduler.status from /api/v2/monitor/health, which is
// the control plane's own view of the advisory-lock leader. ok is false whenever
// that answer cannot be obtained, and the caller degrades to /readyz.
func (c *collector) schedulerStatus(ctx context.Context, base string) (status string, ok bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v2/monitor/health", http.NoBody)
	if err != nil {
		return "", false
	}
	if tok := os.Getenv("SOAK_API_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close of a probe response
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", false
	}
	var payload struct {
		Scheduler struct {
			Status string `json:"status"`
		} `json:"scheduler"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Scheduler.Status == "" {
		return "", false
	}
	return payload.Scheduler.Status, true
}

// counters scrapes the three product counters the soak asserts on. It parses the
// exposition format with a line scanner rather than pulling prometheus/common
// into the module's direct requirements for three numbers.
func (c *collector) counters(ctx context.Context) (stepDowns, undispatchable, atCapacity float64) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.o.apiURL, "/")+"/metrics", http.NoBody)
	if err != nil {
		return math.NaN(), math.NaN(), math.NaN()
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return math.NaN(), math.NaN(), math.NaN()
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close of a scrape response
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 8<<20))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		name, val, ok := splitMetric(line)
		if !ok {
			continue
		}
		switch name {
		case "leoflow_scheduler_step_downs_total":
			stepDowns += val
		case "leoflow_tasks_undispatchable_total":
			undispatchable += val
		case "leoflow_dispatch_at_capacity_total":
			atCapacity += val
		}
	}
	return stepDowns, undispatchable, atCapacity
}

// splitMetric pulls the family name and value out of one exposition line,
// dropping any label set (the soak sums across labels). ok is false for any line
// that is not a sample.
func splitMetric(line string) (name string, value float64, ok bool) {
	sp := strings.LastIndex(line, " ")
	if sp <= 0 {
		return "", 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(line[sp+1:]), 64)
	if err != nil {
		return "", 0, false
	}
	n := line[:sp]
	if b := strings.Index(n, "{"); b >= 0 {
		n = n[:b]
	}
	return strings.TrimSpace(n), v, true
}

// pcts returns p50/p95/p99/max of an unsorted slice, in the slice's own unit.
func pcts(v []float64) (p50, p95, p99, mx float64) {
	if len(v) == 0 {
		return 0, 0, 0, 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	at := func(p float64) float64 {
		i := int(p*float64(len(s)-1) + 0.5)
		if i < 0 {
			i = 0
		}
		if i >= len(s) {
			i = len(s) - 1
		}
		return s[i]
	}
	return at(0.50), at(0.95), at(0.99), s[len(s)-1]
}

// dirBytes sums the apparent size of every regular file under root. Errors are
// swallowed on purpose: a disk reading that fails must not stop the soak, and
// the budget check treats an unreadable directory as zero rather than as a
// reason to tear everything down.
func dirBytes(root string) int64 {
	var total int64
	walk := func(_ string, d os.DirEntry, err error) error {
		// A vanished or unreadable entry is skipped, not propagated: the DAGs are
		// writing into this tree while it is being measured, so a race here is
		// expected and is not a reason to report an unknown size.
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // a transient walk error is a skipped entry, not a failure
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		return total
	}
	return total
}

// freeBytes reports the free space on the filesystem holding path, or -1 when it
// cannot be determined.
func freeBytes(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize) //nolint:gosec,unconvert // platform field widths differ
}
