package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

type fakeVersionRepo struct {
	created bool
	seen    []string
	err     error
}

func (f *fakeVersionRepo) RegisterDagVersion(_ context.Context, _ string, _ domain.DAGSpec, hash string) (bool, error) {
	f.seen = append(f.seen, hash)
	if f.err != nil {
		return false, f.err
	}
	return f.created, nil
}

func versionServer(repo DagVersionRepository) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Versions:      repo,
	})
}

const validSpecJSON = `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`

func TestRegisterVersionCreated(t *testing.T) {
	repo := &fakeVersionRepo{created: true}
	rec := authGet(versionServer(repo), http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var resp versionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DagID != "etl" || !resp.Created || len(resp.SpecHash) != 64 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestRegisterVersionIdempotent(t *testing.T) {
	rec := authGet(versionServer(&fakeVersionRepo{created: false}), http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON)
	if rec.Code != http.StatusOK {
		t.Errorf("idempotent register = %d, want 200", rec.Code)
	}
}

func TestRegisterVersionRejectsMismatchedDagID(t *testing.T) {
	rec := authGet(versionServer(&fakeVersionRepo{}), http.MethodPost, "/api/v2/dags/other/versions", validSpecJSON)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("mismatched dag_id = %d, want 400", rec.Code)
	}
}

// A hand-written spec declaring the removed http_api task type is rejected at
// registration (ADR 0047/0048, #512): the structural guard is that no executor
// exists, and Validate refuses the type before it can be stored.
func TestRegisterVersionRejectsRemovedHTTPAPIType(t *testing.T) {
	spec := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"hook","type":"http_api"}]}`
	rec := authGet(versionServer(&fakeVersionRepo{}), http.MethodPost, "/api/v2/dags/etl/versions", spec)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("removed http_api type = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// A spec that declares a connection (or variable) the tenant has not defined is
// rejected by the repository as domain.ErrValidation (ADR 0055 D6). That is a
// client-fixable input error — the author must run `leoflow connections set` or
// drop the declaration — so the handler must surface it as 400, not 500. Before
// the handleRepoError ErrValidation branch it fell through to 500, which sent
// users to server logs instead of to their own DAG (#724).
func TestRegisterVersionUnknownConnectionReturns400(t *testing.T) {
	repo := &fakeVersionRepo{err: fmt.Errorf(
		"dag %q declares unknown connection(s) %s; define them (leoflow connections set) or remove them from the DAG's connections: declaration: %w",
		"etl", "warehouse", domain.ErrValidation)}
	rec := authGet(versionServer(repo), http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown connection = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestRegisterVersionRejectsInvalidSpec(t *testing.T) {
	bad := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img","tasks":[]}`
	rec := authGet(versionServer(&fakeVersionRepo{}), http.MethodPost, "/api/v2/dags/etl/versions", bad)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid spec = %d, want 400", rec.Code)
	}
}
