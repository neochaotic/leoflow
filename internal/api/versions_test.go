package api

import (
	"context"
	"encoding/json"
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
}

func (f *fakeVersionRepo) RegisterDagVersion(_ context.Context, _ string, _ domain.DAGSpec, hash string) (bool, error) {
	f.seen = append(f.seen, hash)
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

func TestRegisterVersionRejectsLongInlineHTTP(t *testing.T) {
	spec := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"hook","type":"http_api","execution_timeout_seconds":600,` +
		`"http_request":{"method":"POST","url":"https://example.com/h"}}]}`
	rec := authGet(versionServer(&fakeVersionRepo{}), http.MethodPost, "/api/v2/dags/etl/versions", spec)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("long inline http_api = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestRegisterVersionAllowsLongPodHTTP(t *testing.T) {
	spec := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"hook","type":"http_api","execution_mode":"pod","execution_timeout_seconds":3600,` +
		`"http_request":{"method":"POST","url":"https://example.com/h"}}]}`
	rec := authGet(versionServer(&fakeVersionRepo{created: true}), http.MethodPost, "/api/v2/dags/etl/versions", spec)
	if rec.Code != http.StatusCreated {
		t.Errorf("long pod http_api = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
}

func TestRegisterVersionRejectsInvalidSpec(t *testing.T) {
	bad := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img","tasks":[]}`
	rec := authGet(versionServer(&fakeVersionRepo{}), http.MethodPost, "/api/v2/dags/etl/versions", bad)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid spec = %d, want 400", rec.Code)
	}
}
