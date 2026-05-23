package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/domain"
)

// DagVersionLister lists a DAG's registered versions. The Airflow UI fetches
// this to resolve a version_number before requesting version-scoped structure
// (the Graph view); without it the graph never loads. See docs/ui-compatibility.md.
type DagVersionLister interface {
	ListDagVersions(ctx context.Context, tenant, dagID string) ([]domain.DagVersion, error)
}

// dagVersionDTO is the Airflow 3.2.1 DAGVersionResponse. Leoflow has no bundle
// concept, so bundle_version/bundle_url are null and bundle_name is a constant.
type dagVersionDTO struct {
	ID             string    `json:"id"`
	VersionNumber  int       `json:"version_number"`
	DagID          string    `json:"dag_id"`
	BundleName     string    `json:"bundle_name"`
	BundleVersion  *string   `json:"bundle_version"`
	CreatedAt      time.Time `json:"created_at"`
	DagDisplayName string    `json:"dag_display_name"`
	BundleURL      *string   `json:"bundle_url"`
}

type dagVersionCollectionDTO struct {
	DagVersions  []dagVersionDTO `json:"dag_versions"`
	TotalEntries int             `json:"total_entries"`
}

// dagVersionsHandler implements GET /api/v2/dags/{dag_id}/dagVersions.
func dagVersionsHandler(lister DagVersionLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Param("dag_id")
		versions, err := lister.ListDagVersions(c.Request.Context(), tenantOf(c), dagID)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := dagVersionCollectionDTO{
			DagVersions:  make([]dagVersionDTO, 0, len(versions)),
			TotalEntries: len(versions),
		}
		for _, v := range versions {
			out.DagVersions = append(out.DagVersions, dagVersionDTO{
				ID:             v.ID,
				VersionNumber:  v.VersionNumber,
				DagID:          dagID,
				BundleName:     "leoflow",
				CreatedAt:      v.CreatedAt,
				DagDisplayName: dagID,
			})
		}
		c.JSON(http.StatusOK, out)
	}
}
