package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// The public /api/v2 endpoints below back Airflow features Leoflow does not
// implement yet (tags, warnings, import errors, assets, plugins, pools,
// human-in-the-loop). The 3.2.1 UI polls them on the DAG list and detail
// screens; a 404 surfaces as a broken detail view and console errors. Each
// returns a schema-valid empty collection so the UI degrades gracefully. Real
// implementations are tracked per endpoint (GitHub issues #26–#32).

// apiEmptyCollection renders a {<field>:[], total_entries:0} envelope.
func apiEmptyCollection(field string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{field: []any{}, "total_entries": 0})
	}
}

// registerAPIStubs mounts graceful empty responses for the unimplemented public
// /api/v2 list endpoints the Airflow UI polls.
func registerAPIStubs(r gin.IRouter) {
	r.GET("/api/v2/dagTags", apiEmptyCollection("tags"))             // #26
	r.GET("/api/v2/dagWarnings", apiEmptyCollection("dag_warnings")) // #27
	// /api/v2/importErrors is owned by registerImportErrors (real when an
	// ImportErrorStore is set, empty stub otherwise) — see #28.
	r.GET("/api/v2/plugins/importErrors", apiEmptyCollection("import_errors")) // #28
	r.GET("/api/v2/assets", apiEmptyCollection("assets"))                      // #29
	r.GET("/api/v2/assets/events", apiEmptyCollection("asset_events"))         // #29
	r.GET("/api/v2/plugins", apiEmptyCollection("plugins"))                    // #30
	r.GET("/api/v2/pools", apiEmptyCollection("pools"))                        // #31
	r.GET("/api/v2/providers", apiEmptyCollection("providers"))                // #30
	r.GET("/api/v2/jobs", apiEmptyCollection("jobs"))                          // #30
	// /api/v2/eventLogs is owned by registerUIAudit (real when an AuditLogReader
	// is set, empty stub otherwise) — see #37.
	// Human-in-the-loop details, polled at the DAG-run level (#32).
	r.GET("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/hitlDetails", apiEmptyCollection("hitl_details"))
}
