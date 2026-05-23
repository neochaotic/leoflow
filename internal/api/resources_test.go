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
	dags []domain.DAG
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

type fakeTaskRepo struct{ tis []domain.TaskInstance }

func (f *fakeTaskRepo) ListTaskInstances(context.Context, string, string, string, int, int) ([]domain.TaskInstance, int, error) {
	return f.tis, len(f.tis), nil
}
func (f *fakeTaskRepo) ClearTaskInstances(context.Context, string, string, string, []string, bool) (int, error) {
	return len(f.tis), nil
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
