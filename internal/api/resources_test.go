package api

import (
	"context"
	"encoding/json"
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
