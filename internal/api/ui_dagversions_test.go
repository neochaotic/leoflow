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

type fakeVersionLister struct{ versions []domain.DagVersion }

func (f *fakeVersionLister) ListDagVersions(context.Context, string, string) ([]domain.DagVersion, error) {
	return f.versions, nil
}

func versionsServer(dags []domain.DAG, versions []domain.DagVersion) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Dags:          &fakeDagRepo{dags: dags},
		DagVersions:   &fakeVersionLister{versions: versions},
	})
}

func TestDagVersionsEndpoint(t *testing.T) {
	srv := versionsServer([]domain.DAG{{DagID: "etl"}},
		[]domain.DagVersion{{ID: "v-uuid", VersionNumber: 1, CreatedAt: time.Now().UTC()}})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagVersions?order_by=-version_number", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/dagVersions = %d (%s)", rec.Code, rec.Body.String())
	}
	var col struct {
		DagVersions []map[string]any `json:"dag_versions"`
		Total       int              `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.Total != 1 || len(col.DagVersions) != 1 {
		t.Fatalf("want 1 version, got total=%d len=%d", col.Total, len(col.DagVersions))
	}
	v := col.DagVersions[0]
	// version_number drives the Graph view's structure_data fetch — must be present.
	if v["version_number"].(float64) != 1 {
		t.Errorf("version_number = %v, want 1", v["version_number"])
	}
	for _, f := range []string{"id", "dag_id", "bundle_name", "created_at", "dag_display_name", "bundle_version", "bundle_url"} {
		if _, ok := v[f]; !ok {
			t.Errorf("dag version missing %q", f)
		}
	}
}

func TestDagDetailsPopulatesLatestDagVersion(t *testing.T) {
	// The Graph view reads details.latest_dag_version.version_number to fetch
	// version-scoped structure; a null version leaves the graph blank.
	srv := versionsServer([]domain.DAG{{DagID: "etl"}},
		[]domain.DagVersion{{ID: "v-uuid", VersionNumber: 1, CreatedAt: time.Now().UTC()}})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/details", "")
	var d struct {
		Latest *struct {
			VersionNumber int `json:"version_number"`
		} `json:"latest_dag_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Latest == nil || d.Latest.VersionNumber != 1 {
		t.Errorf("latest_dag_version not populated: %s", rec.Body.String())
	}
}
