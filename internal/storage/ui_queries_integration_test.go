//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/storage"
)

// openRepo connects to the migrated test database or skips. It returns the
// Repository (UI/resource queries) and SchedulerStore (task materialization)
// over the same pool, plus a cleanup.
func openRepo(t *testing.T) (*storage.Repository, *storage.SchedulerStore, context.Context) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	return storage.NewRepository(pg), storage.NewSchedulerStore(pg), ctx
}

func registerSpec(t *testing.T, repo *storage.Repository, ctx context.Context, dagID string, tasks []domain.TaskSpec) {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: tasks,
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
		t.Fatalf("register %s: created=%v err=%v", dagID, created, rerr)
	}
}

func TestGetCurrentSpecIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_spec_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{
		{TaskID: "extract", Type: domain.TaskTypePython},
		{TaskID: "load", Type: domain.TaskTypePython, DependsOn: []string{"extract"}},
	}
	registerSpec(t, repo, ctx, dagID, tasks)

	got, err := repo.GetCurrentSpec(ctx, "default", dagID)
	if err != nil {
		t.Fatalf("GetCurrentSpec: %v", err)
	}
	if got.DagID != dagID || len(got.Tasks) != 2 {
		t.Fatalf("spec mismatch: dag=%s tasks=%d", got.DagID, len(got.Tasks))
	}
	if got.Tasks[1].TaskID != "load" || len(got.Tasks[1].DependsOn) != 1 {
		t.Errorf("dependencies not round-tripped: %+v", got.Tasks[1])
	}

	if _, err := repo.GetCurrentSpec(ctx, "default", "does-not-exist"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing dag = %v, want ErrNotFound", err)
	}
}

func TestLatestRunsForDagsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	base := time.Now().UnixNano()
	dagA := fmt.Sprintf("uiq_lr_a_%d", base)
	dagB := fmt.Sprintf("uiq_lr_b_%d", base)
	for _, id := range []string{dagA, dagB} {
		registerSpec(t, repo, ctx, id, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})
		// three runs, ascending logical date; latest must come back first.
		for i := 0; i < 3; i++ {
			logical := time.Unix(0, base).Add(time.Duration(i) * time.Hour).UTC()
			if _, err := repo.CreateDagRun(ctx, "default", id, domain.DagRun{
				RunID: fmt.Sprintf("r%d", i), State: domain.DagRunStateSuccess,
				RunType: "manual", LogicalDate: logical, QueuedAt: logical,
			}); err != nil {
				t.Fatalf("create run: %v", err)
			}
		}
	}

	byDag, err := repo.LatestRunsForDags(ctx, "default", []string{dagA, dagB}, 2)
	if err != nil {
		t.Fatalf("LatestRunsForDags: %v", err)
	}
	for _, id := range []string{dagA, dagB} {
		runs := byDag[id]
		if len(runs) != 2 {
			t.Fatalf("%s: want 2 latest runs, got %d", id, len(runs))
		}
		// LIMIT 2 of [r0,r1,r2] by logical_date DESC -> r2, r1.
		if runs[0].RunID != "r2" || runs[1].RunID != "r1" {
			t.Errorf("%s: latest runs out of order: %s, %s", id, runs[0].RunID, runs[1].RunID)
		}
	}

	// Empty input is a no-op (no query), not an error.
	empty, err := repo.LatestRunsForDags(ctx, "default", nil, 5)
	if err != nil || len(empty) != 0 {
		t.Errorf("empty dagIDs = %v, %v", empty, err)
	}
}

func TestTaskInstancesForRunsIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_ti_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{
		{TaskID: "extract", Type: domain.TaskTypePython},
		{TaskID: "transform", Type: domain.TaskTypePython, DependsOn: []string{"extract"}},
		{TaskID: "load", Type: domain.TaskTypePython, DependsOn: []string{"transform"}},
	}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Resolve the run UUID (MaterializeTasks keys by it) via the active-runs view.
	runUUID := ""
	runs, raErr := sched.ActiveRuns(ctx)
	if raErr != nil {
		t.Fatalf("ActiveRuns: %v", raErr)
	}
	for _, r := range runs {
		if r.DagID == dagID {
			runUUID = r.RunID
		}
	}
	if runUUID == "" {
		t.Fatal("could not resolve run UUID for the new run")
	}
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "extract", domain.TaskStateSuccess); err != nil {
		t.Fatal(err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "transform", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}
	// "load" stays none.

	tis, err := repo.TaskInstancesForRuns(ctx, "default", dagID, []string{"r1"})
	if err != nil {
		t.Fatalf("TaskInstancesForRuns: %v", err)
	}
	if len(tis) != 3 {
		t.Fatalf("want 3 task instances, got %d", len(tis))
	}
	states := map[string]domain.TaskState{}
	for _, ti := range tis {
		if ti.RunID != "r1" {
			t.Errorf("ti %s carries wrong run_id %q", ti.TaskID, ti.RunID)
		}
		states[ti.TaskID] = ti.State
	}
	if states["extract"] != domain.TaskStateSuccess || states["transform"] != domain.TaskStateRunning || states["load"] != domain.TaskStateNone {
		t.Errorf("unexpected states: %v", states)
	}

	// Unknown run_ids yield no rows; empty run_ids skip the query entirely.
	if got, _ := repo.TaskInstancesForRuns(ctx, "default", dagID, []string{"nope"}); len(got) != 0 {
		t.Errorf("unknown run_id returned %d rows", len(got))
	}
	if got, _ := repo.TaskInstancesForRuns(ctx, "default", dagID, nil); got != nil {
		t.Errorf("empty run_ids returned %v", got)
	}
}

// TestListDagVersionsIntegration guards the row_number()-based version_number
// (drives the Graph view's version-scoped structure fetch) against real Postgres.
func TestListDagVersionsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_ver_%d", time.Now().UnixNano())

	// Two distinct specs for the same DAG -> two versions (RegisterDagVersion
	// dedups by spec hash, so the second must differ).
	for i, ver := range []string{"v1", "v2"} {
		spec := domain.DAGSpec{
			SchemaVersion: "1.0", DagID: dagID, DagVersion: ver, Image: "img:" + ver,
			Tasks: []domain.TaskSpec{{TaskID: fmt.Sprintf("t%d", i), Type: domain.TaskTypePython}},
		}
		hash, err := spec.CanonicalHash()
		if err != nil {
			t.Fatal(err)
		}
		if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
			t.Fatalf("register %s: created=%v err=%v", ver, created, rerr)
		}
	}

	versions, err := repo.ListDagVersions(ctx, "default", dagID)
	if err != nil {
		t.Fatalf("ListDagVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("want 2 versions, got %d", len(versions))
	}
	// Newest-first: version_number 2 then 1.
	if versions[0].VersionNumber != 2 || versions[1].VersionNumber != 1 {
		t.Errorf("version_number ordering wrong: %d, %d", versions[0].VersionNumber, versions[1].VersionNumber)
	}
	if versions[0].ID == "" || versions[0].CreatedAt.IsZero() {
		t.Errorf("version row missing id/created_at: %+v", versions[0])
	}
}

// TestDashboardStatsIntegration guards the home-dashboard counters: DAGs by
// latest-run state and run/task-instance state counts within a time window.
func TestDashboardStatsIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	base := time.Now().UnixNano()
	now := time.Now().UTC()
	dagFail := fmt.Sprintf("uiq_dash_fail_%d", base)
	dagRun := fmt.Sprintf("uiq_dash_run_%d", base)
	tasks := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython, DependsOn: []string{"a"}},
	}

	// Create every run as running first (so ActiveRuns resolves its UUID and
	// MaterializeTasks works), then transition the run to its target state.
	mkRun := func(dagID string) string {
		registerSpec(t, repo, ctx, dagID, tasks)
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: now,
		}); err != nil {
			t.Fatalf("create run: %v", err)
		}
		runs, err := sched.ActiveRuns(ctx)
		if err != nil {
			t.Fatalf("ActiveRuns: %v", err)
		}
		for _, r := range runs {
			if r.DagID == dagID {
				if merr := sched.MaterializeTasks(ctx, r.RunID, tasks); merr != nil {
					t.Fatalf("MaterializeTasks: %v", merr)
				}
				return r.RunID
			}
		}
		t.Fatalf("run UUID not resolved for %s", dagID)
		return ""
	}

	failUUID := mkRun(dagFail)
	if err := sched.ApplyTransition(ctx, failUUID, "a", domain.TaskStateSuccess); err != nil {
		t.Fatal(err)
	}
	if err := sched.ApplyTransition(ctx, failUUID, "b", domain.TaskStateFailed); err != nil {
		t.Fatal(err)
	}
	if err := sched.SetRunState(ctx, failUUID, domain.DagRunStateFailed); err != nil {
		t.Fatal(err)
	}
	runUUID := mkRun(dagRun)
	if err := sched.ApplyTransition(ctx, runUUID, "a", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}

	stats, err := repo.DagStats(ctx, "default")
	if err != nil {
		t.Fatalf("DagStats: %v", err)
	}
	if stats.Active < 2 {
		t.Errorf("active dag count = %d, want >= 2", stats.Active)
	}
	if stats.Failed < 1 || stats.Running < 1 {
		t.Errorf("dag-by-latest-state: failed=%d running=%d, want both >= 1", stats.Failed, stats.Running)
	}

	m, err := repo.HistoricalMetrics(ctx, "default", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("HistoricalMetrics: %v", err)
	}
	if m.RunStates["failed"] < 1 || m.RunStates["running"] < 1 {
		t.Errorf("run states = %v, want failed>=1 running>=1", m.RunStates)
	}
	if m.TIStates["success"] < 1 || m.TIStates["failed"] < 1 || m.TIStates["running"] < 1 {
		t.Errorf("ti states = %v, want success/failed/running >= 1 each", m.TIStates)
	}
}

// TestReportStateRecordsResultIntegration guards the pod-agent state-report path
// (ExecutionStore.ReportState -> ReportTaskResult). Before the $3::task_state
// cast, this query failed with SQLSTATE 42P08 (inconsistent types deduced for
// the state parameter); the pod path was the first to exercise it.
func TestReportStateRecordsResultIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	exec := storage.NewExecutionStore(pg)

	dagID := fmt.Sprintf("uiq_report_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	runUUID := ""
	runs, raErr := sched.ActiveRuns(ctx)
	if raErr != nil {
		t.Fatalf("ActiveRuns: %v", raErr)
	}
	for _, r := range runs {
		if r.DagID == dagID {
			runUUID = r.RunID
		}
	}
	if runUUID == "" {
		t.Fatal("could not resolve run UUID")
	}
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}

	id := auth.AgentIdentity{RunID: runUUID, TaskID: "t"}
	// running then success — exactly the transitions the agent reports; both
	// must record without a type-deduction error.
	if err := exec.ReportState(ctx, id, domain.TaskStateRunning, 0, ""); err != nil {
		t.Fatalf("ReportState(running): %v", err)
	}
	if err := exec.ReportState(ctx, id, domain.TaskStateSuccess, 0, ""); err != nil {
		t.Fatalf("ReportState(success): %v", err)
	}

	tis, _, err := repo.ListTaskInstances(ctx, "default", dagID, "r1", 10, 0)
	if err != nil {
		t.Fatalf("ListTaskInstances: %v", err)
	}
	if len(tis) != 1 || tis[0].State != domain.TaskStateSuccess {
		t.Fatalf("want 1 task instance in success, got %+v", tis)
	}
	if tis[0].EndedAt == nil {
		t.Errorf("success report should set ended_at")
	}
}
