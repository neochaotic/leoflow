package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

type fakeDagRepo struct {
	dags        []domain.DAG
	gotRunState string
	gotPaused   *bool
	cleared     []string
}

func (f *fakeDagRepo) ListDags(context.Context, string, int, int) ([]domain.DAG, int, error) {
	return f.dags, len(f.dags), nil
}

func (f *fakeDagRepo) GetDag(_ context.Context, _, dagID string) (domain.DAG, error) {
	for _, d := range f.dags {
		if d.DagID == dagID {
			return d, nil
		}
	}
	return domain.DAG{}, ErrNotFound
}

func (f *fakeDagRepo) SetPaused(_ context.Context, _, dagID string, paused bool) (domain.DAG, error) {
	for _, d := range f.dags {
		if d.DagID == dagID {
			d.IsPaused = paused
			return d, nil
		}
	}
	return domain.DAG{}, ErrNotFound
}

func (f *fakeDagRepo) ListDagsFiltered(_ context.Context, _, runState string, paused *bool, _, _ int) ([]domain.DAG, int, error) {
	f.gotRunState = runState
	f.gotPaused = paused
	out := []domain.DAG{}
	for _, d := range f.dags {
		if paused != nil && d.IsPaused != *paused {
			continue
		}
		out = append(out, d)
	}
	return out, len(out), nil
}

func (f *fakeDagRepo) DeleteDag(_ context.Context, _, dagID string) error {
	for i, d := range f.dags {
		if d.DagID == dagID {
			f.dags = append(f.dags[:i], f.dags[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeDagRepo) ClearDagHistory(_ context.Context, _, dagID string) error {
	for _, d := range f.dags {
		if d.DagID == dagID {
			f.cleared = append(f.cleared, dagID) // keeps the DAG, records the clear
			return nil
		}
	}
	return ErrNotFound
}

type fakeRunRepo struct{ runs []domain.DagRun }

func (f *fakeRunRepo) ListDagRuns(context.Context, string, string, int, int) ([]domain.DagRun, int, error) {
	return f.runs, len(f.runs), nil
}
func (f *fakeRunRepo) GetDagRun(_ context.Context, _, _, runID string) (domain.DagRun, error) {
	for _, r := range f.runs {
		if r.RunID == runID {
			return r, nil
		}
	}
	return domain.DagRun{}, ErrNotFound
}
func (f *fakeRunRepo) SetDagRunState(_ context.Context, _, _, runID, state string) error {
	for i := range f.runs {
		if f.runs[i].RunID == runID {
			f.runs[i].State = domain.DagRunState(state)
		}
	}
	return nil
}
func (f *fakeRunRepo) DeleteDagRun(_ context.Context, _, _, runID string) error {
	for i := range f.runs {
		if f.runs[i].RunID == runID {
			f.runs = append(f.runs[:i], f.runs[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}
func (f *fakeRunRepo) CreateDagRun(_ context.Context, _, dagID string, run domain.DagRun) (domain.DagRun, error) {
	run.DagID = dagID
	return run, nil
}

type fakeTaskRepo struct {
	tis           []domain.TaskInstance
	gotOnlyFailed bool
	setState      string
	setTaskID     string
}

func (f *fakeTaskRepo) ListTaskInstances(context.Context, string, string, string, int, int) ([]domain.TaskInstance, int, error) {
	return f.tis, len(f.tis), nil
}
func (f *fakeTaskRepo) ClearTaskInstances(_ context.Context, _, _, _ string, _ []string, onlyFailed, _ bool) (int, error) {
	f.gotOnlyFailed = onlyFailed
	return len(f.tis), nil
}
func (f *fakeTaskRepo) SetTaskInstanceState(_ context.Context, _, _, _, taskID, state string) error {
	f.setTaskID, f.setState = taskID, state
	return nil
}

func dagOnlyServer(repo DagRepository) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Dags:          repo,
	})
}

func TestClearIncludePastFutureFansAcrossRuns(t *testing.T) {
	now := time.Now().UTC()
	runs := &fakeRunRepo{runs: []domain.DagRun{
		{DagID: "etl", RunID: "past", LogicalDate: now.Add(-2 * time.Hour)},
		{DagID: "etl", RunID: "cur", LogicalDate: now},
		{DagID: "etl", RunID: "future", LogicalDate: now.Add(2 * time.Hour)},
	}}
	// fakeTaskRepo returns its single TI for any run, so the affected count equals
	// the number of target runs the clear fans out to.
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", State: domain.TaskStateFailed}}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		Tasks: tasks, DagRuns: runs,
	})
	count := func(extra string) int {
		body := `{"dag_run_id":"cur","task_ids":["extract"],"dry_run":true` + extra + `}`
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", body)
		var got struct {
			TotalEntries int `json:"total_entries"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return got.TotalEntries
	}
	if n := count(""); n != 1 {
		t.Errorf("no flags: %d target runs, want 1 (cur only)", n)
	}
	if n := count(`,"include_past":true`); n != 2 {
		t.Errorf("include_past: %d, want 2 (cur+past)", n)
	}
	if n := count(`,"include_future":true`); n != 2 {
		t.Errorf("include_future: %d, want 2 (cur+future)", n)
	}
	if n := count(`,"include_past":true,"include_future":true`); n != 3 {
		t.Errorf("past+future: %d, want 3 (all runs)", n)
	}
}

func TestExpandTaskIDs(t *testing.T) {
	// a -> b -> c (c depends on b, b depends on a), plus a sibling d off a.
	tasks := []domain.TaskSpec{
		{TaskID: "a"},
		{TaskID: "b", DependsOn: []string{"a"}},
		{TaskID: "c", DependsOn: []string{"b"}},
		{TaskID: "d", DependsOn: []string{"a"}},
	}
	set := func(ids []string) map[string]bool {
		m := map[string]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	// seeds only.
	if got := set(expandTaskIDs(tasks, []string{"b"}, false, false)); len(got) != 1 || !got["b"] {
		t.Errorf("seeds-only = %v, want {b}", got)
	}
	// downstream of a = a,b,c,d (transitive).
	got := set(expandTaskIDs(tasks, []string{"a"}, false, true))
	for _, id := range []string{"a", "b", "c", "d"} {
		if !got[id] {
			t.Errorf("downstream(a) missing %s: %v", id, got)
		}
	}
	// upstream of c = c,b,a (transitive), not d.
	got = set(expandTaskIDs(tasks, []string{"c"}, true, false))
	if !got["a"] || !got["b"] || !got["c"] || got["d"] {
		t.Errorf("upstream(c) = %v, want {a,b,c}", got)
	}
}

func TestMarkDagRunState(t *testing.T) {
	runs := &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1", State: domain.DagRunStateRunning}}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		DagRuns: runs,
	})
	rec := authGet(srv, http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1", `{"state":"failed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mark run = %d (%s)", rec.Code, rec.Body.String())
	}
	if runs.runs[0].State != domain.DagRunStateFailed {
		t.Errorf("run state = %q, want failed", runs.runs[0].State)
	}
	if rec := authGet(srv, http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1", `{"state":"banana"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid run state = %d, want 400", rec.Code)
	}
}

func TestMarkTaskInstanceState(t *testing.T) {
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed}}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		Tasks: tasks, DagRuns: &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1"}}},
	})
	// Real PATCH applies the state.
	rec := authGet(srv, http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/-1", `{"new_state":"success"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mark = %d (%s)", rec.Code, rec.Body.String())
	}
	if tasks.setTaskID != "extract" || tasks.setState != "success" {
		t.Errorf("expected set extract->success, got %q->%q", tasks.setTaskID, tasks.setState)
	}
	// dry_run must NOT apply (the recorded state stays from the prior call).
	tasks.setState = ""
	rec = authGet(srv, http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/dry_run", `{"new_state":"failed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry_run = %d (%s)", rec.Code, rec.Body.String())
	}
	if tasks.setState != "" {
		t.Errorf("dry_run must not change state, but set %q", tasks.setState)
	}
	// An invalid state is rejected.
	if rec := authGet(srv, http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/-1", `{"new_state":"banana"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid new_state = %d, want 400", rec.Code)
	}
}

func TestClearTaskInstancesDryRun(t *testing.T) {
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{
		{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed},
		{TaskID: "load", RunID: "r1", State: domain.TaskStateSuccess},
	}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		Tasks: tasks, DagRuns: &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1"}}},
	})
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", `{"dag_run_id":"r1","only_failed":true,"dry_run":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry_run clear = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		TaskInstances []map[string]any `json:"task_instances"`
		TotalEntries  int              `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Only the failed task is affected, and dry_run did not actually clear.
	if got.TotalEntries != 1 || got.TaskInstances[0]["task_id"] != "extract" {
		t.Errorf("dry_run affected = %+v, want [extract]", got.TaskInstances)
	}
}

func authedServer() *gin.Engine {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	sched := "0 5 * * *"
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: admin},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
		Dags:          &fakeDagRepo{dags: []domain.DAG{{DagID: "etl", Owner: "data", Schedule: &sched, Tags: []string{"x"}}}},
		DagRuns:       &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1", State: domain.DagRunStateQueued}}},
		Tasks:         &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", RunID: "r1", State: domain.TaskStateQueued}}},
	})
}

func authGet(srv *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequestWithContext(context.Background(), method, path, http.NoBody)
	} else {
		r = httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

func TestDeleteDagRun(t *testing.T) {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	runs := &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1", State: domain.DagRunStateSuccess}}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: admin},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		DagRuns: runs,
	})
	// The UI's "delete run" must succeed (204), not 404 as "API route not found".
	rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl/dagRuns/r1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE existing run = %d, want 204; body=%q", rec.Code, rec.Body.String())
	}
	if len(runs.runs) != 0 {
		t.Errorf("run was not removed: %+v", runs.runs)
	}
	// A missing run is a clean 404, not a silent 204.
	if rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl/dagRuns/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE missing run = %d, want 404", rec.Code)
	}
}

func TestListDagRunsStateFilter(t *testing.T) {
	// The "failed runs" widget filters with ?state=failed; the handler must honor
	// it (it previously ignored the filter and returned every run).
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: admin},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		DagRuns: &fakeRunRepo{runs: []domain.DagRun{
			{DagID: "etl", RunID: "r1", State: domain.DagRunStateSuccess},
			{DagID: "etl", RunID: "r2", State: domain.DagRunStateFailed},
			{DagID: "etl", RunID: "r3", State: domain.DagRunStateSuccess},
		}},
	})
	decode := func(rec *httptest.ResponseRecorder) int {
		var got struct {
			TotalEntries int `json:"total_entries"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got.TotalEntries
	}
	if n := decode(authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns?state=failed", "")); n != 1 {
		t.Errorf("state=failed total = %d, want 1", n)
	}
	if n := decode(authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns?state=success", "")); n != 2 {
		t.Errorf("state=success total = %d, want 2", n)
	}
	if n := decode(authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns", "")); n != 3 {
		t.Errorf("no filter total = %d, want 3", n)
	}
}

func TestDeleteDagClearsHistoryByDefault(t *testing.T) {
	// The trash button (plain DELETE) clears history but KEEPS the DAG (ADR 0020),
	// because a GitOps DAG would not reload after a destructive delete.
	repo := &fakeDagRepo{dags: []domain.DAG{{DagID: "etl"}}}
	srv := dagOnlyServer(repo)
	rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("clear = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(repo.cleared) != 1 || repo.cleared[0] != "etl" {
		t.Errorf("expected a clear of etl, got %v", repo.cleared)
	}
	// The DAG still exists, so a second clear also succeeds (not 404).
	if rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl", ""); rec.Code != http.StatusNoContent {
		t.Errorf("second clear = %d, want 204 (dag still registered)", rec.Code)
	}
	if len(repo.dags) != 1 {
		t.Errorf("clear must keep the DAG registered, got %d dags", len(repo.dags))
	}
}

func TestDeleteDagDeregisterRemoves(t *testing.T) {
	repo := &fakeDagRepo{dags: []domain.DAG{{DagID: "etl"}}}
	srv := dagOnlyServer(repo)
	rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl?deregister=true", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("deregister = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(repo.dags) != 0 {
		t.Errorf("deregister must remove the DAG, got %d dags", len(repo.dags))
	}
	// Now it is gone -> 404.
	if rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl?deregister=true", ""); rec.Code != http.StatusNotFound {
		t.Errorf("deregister missing dag = %d, want 404", rec.Code)
	}
}

func TestListDags(t *testing.T) {
	rec := authGet(authedServer(), http.MethodGet, "/api/v2/dags", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list dags = %d (%s)", rec.Code, rec.Body.String())
	}
	var col dagCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.TotalEntries != 1 || len(col.Dags) != 1 || col.Dags[0].DagID != "etl" {
		t.Errorf("unexpected collection: %+v", col)
	}
	if col.Dags[0].ScheduleInterval == nil || col.Dags[0].ScheduleInterval.Value != "0 5 * * *" {
		t.Errorf("schedule interval not translated: %+v", col.Dags[0].ScheduleInterval)
	}
}

func TestGetDagFoundAndNotFound(t *testing.T) {
	srv := authedServer()
	if rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl", ""); rec.Code != http.StatusOK {
		t.Errorf("get etl = %d, want 200", rec.Code)
	}
	if rec := authGet(srv, http.MethodGet, "/api/v2/dags/missing", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get missing = %d, want 404", rec.Code)
	}
}

func TestPatchDagPause(t *testing.T) {
	rec := authGet(authedServer(), http.MethodPatch, "/api/v2/dags/etl", `{"is_paused":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d", rec.Code)
	}
	var d dagDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if !d.IsPaused {
		t.Error("is_paused should be true after patch")
	}
}

func TestDagRunsListAndCreate(t *testing.T) {
	srv := authedServer()
	if rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns", ""); rec.Code != http.StatusOK {
		t.Errorf("list runs = %d", rec.Code)
	}
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{"dag_run_id":"manual__1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create run = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var run dagRunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.DagRunID != "manual__1" || run.State != "queued" || run.RunType != "manual" {
		t.Errorf("unexpected run: %+v", run)
	}
	// dag_versions is a required array the run view maps over; a missing/nil one
	// crashes the UI with "undefined.map".
	if run.DagVersions == nil {
		t.Error("dag run response must include dag_versions (array), got nil")
	}
}

func TestTaskInstancesWildcardRunReturnsEmpty(t *testing.T) {
	// The overview polls dagRuns/~/taskInstances (all runs); must degrade to an
	// empty collection (200), not 404.
	rec := authGet(authedServer(), http.MethodGet, "/api/v2/dags/etl/dagRuns/~/taskInstances", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("~/taskInstances = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"total_entries":0`) {
		t.Errorf("~/taskInstances should be empty, got %s", rec.Body.String())
	}
}

func TestDagRunsWildcardReturnsEmptyCollection(t *testing.T) {
	// The UI home polls /api/v2/dags/~/dagRuns (all DAGs). It must degrade to an
	// empty collection (200), not 404 — "~" is not a real DAG.
	rec := authGet(authedServer(), http.MethodGet, "/api/v2/dags/~/dagRuns", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("~/dagRuns = %d, want 200", rec.Code)
	}
	var col dagRunCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.TotalEntries != 0 || len(col.DagRuns) != 0 {
		t.Errorf("~/dagRuns should be empty, got %+v", col)
	}
}

func TestDagRunCreateGeneratesRunID(t *testing.T) {
	rec := authGet(authedServer(), http.MethodPost, "/api/v2/dags/etl/dagRuns", `{}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create run with empty body = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var run dagRunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(run.DagRunID, "manual__") {
		t.Errorf("an unspecified dag_run_id should be auto-generated, got %q", run.DagRunID)
	}
}

func TestTaskInstancesAndClear(t *testing.T) {
	srv := authedServer()
	if rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns/r1/taskInstances", ""); rec.Code != http.StatusOK {
		t.Errorf("list task instances = %d", rec.Code)
	}
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", `{"task_ids":["extract"],"dag_run_id":"r1"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("clear = %d, want 200", rec.Code)
	}
}

// TestClearAcceptsAirflowTupleTaskIDs is a CONTRACT test against the REAL Airflow
// 3.2 UI payload: the SPA sends task_ids as [task_id, map_index] tuples (captured
// from the bundle as task_ids:[[...]]), not plain strings. The clear endpoint must
// accept that shape. The prior []string DTO 400'd on it — and the older tests used
// our own invented `["extract"]` shape, so they never caught the mismatch (issue
// #98). This test posts the real shape, so it fails if the contract breaks again.
func TestClearAcceptsAirflowTupleTaskIDs(t *testing.T) {
	srv := authedServer()
	// Real Airflow shape: each task_ids element is a [task_id, map_index] tuple.
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","task_ids":[["extract",-1]],"dry_run":true}`)
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("the real Airflow tuple payload must bind, not 400: %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("clear (tuple task_ids) = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Backward compatibility: plain-string task_ids still bind.
	if rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","task_ids":["extract"]}`); rec.Code != http.StatusOK {
		t.Errorf("string task_ids should still bind, got %d", rec.Code)
	}
}

// TestTaskInstanceActionMapIndexSubresources is a CONTRACT test against the real
// Airflow 3.2 SPA, captured live from a browser: right after a Clear, the UI
// requests the attempts list as "{map_index}/tries" (e.g. "-1/tries"). The
// catch-all only knew "tries"/"tries/{n}", so "-1/tries" fell through to
// strconv.Atoi and 400'd with the misleading "map_index must be an integer".
// This asserts the real URL shape works and that a genuinely unknown action gets
// a clear 404 — never that misleading 400.
func TestTaskInstanceActionMapIndexSubresources(t *testing.T) {
	srv := authedServer()
	base := "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract"

	// The exact shape the SPA sends after a Clear (was a 400 in the wild).
	if rec := authGet(srv, http.MethodGet, base+"/-1/tries", ""); rec.Code != http.StatusOK {
		t.Errorf("GET {map_index}/tries = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// The bare map_index still returns the single instance (the fake's TI is at
	// map_index 0).
	if rec := authGet(srv, http.MethodGet, base+"/0", ""); rec.Code != http.StatusOK {
		t.Errorf("GET {map_index} = %d, want 200", rec.Code)
	}
	// A genuinely unknown action must NOT masquerade as a map_index error.
	rec := authGet(srv, http.MethodGet, base+"/dependencies", "")
	if strings.Contains(rec.Body.String(), "map_index must be an integer") {
		t.Errorf("unknown action misreported as a map_index error: %s", rec.Body.String())
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown action = %d, want 404", rec.Code)
	}
}

// TestHandleRepoErrorClientCancel guards the ti_summaries 500 regression: a
// canceled/timed-out context means the client aborted (the UI supersedes in-flight
// grid requests), which must map to 499 (client closed), not a 500 server fault.
func TestHandleRepoErrorClientCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"client canceled", context.Canceled, statusClientClosedRequest},
		{"deadline exceeded", context.DeadlineExceeded, statusClientClosedRequest},
		{"wrapped cancel", errors.Join(errors.New("task instances for runs"), context.Canceled), statusClientClosedRequest},
		{"not found", ErrNotFound, http.StatusNotFound},
		{"real server error", errors.New("db exploded"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", http.NoBody)
		handleRepoError(c, tc.err)
		if w.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, w.Code, tc.want)
		}
	}
}

func TestClearOnlyFailedForwarded(t *testing.T) {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	repo := &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed}}}
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: admin},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Tasks:         repo,
	})

	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","only_failed":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear only_failed = %d (%s)", rec.Code, rec.Body.String())
	}
	if !repo.gotOnlyFailed {
		t.Error("only_failed=true not forwarded to repository")
	}

	repo.gotOnlyFailed = true // ensure the next call actually flips it back
	rec = authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"task_ids":["extract"],"dag_run_id":"r1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear default = %d", rec.Code)
	}
	if repo.gotOnlyFailed {
		t.Error("only_failed should default to false when omitted")
	}
}

func TestXComStub(t *testing.T) {
	rec := authGet(authedServer(), http.MethodGet, "/api/v2/xcoms/etl/r1/extract/key", "")
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("xcom stub = %d, want 501", rec.Code)
	}
}

func TestListDagsLinkHeader(t *testing.T) {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: admin},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Dags:          &fakeDagRepo{dags: []domain.DAG{{DagID: "a"}, {DagID: "b"}}},
	})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags?limit=1&offset=0", "")
	if link := rec.Header().Get("Link"); !strings.Contains(link, `rel="next"`) {
		t.Errorf("Link = %q, want a next relation", link)
	}
}
