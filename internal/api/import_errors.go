package api

import (
	"context"
	"hash/crc32"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/domain"
)

// ImportErrorStore reads and writes DAG parse/compile errors that back Airflow's
// "Import Errors" banner on the home dashboard. The `leoflow dev` watcher writes
// an entry on a failed compile and clears it on the next good compile; the
// public GET /api/v2/importErrors feed is what the UI polls.
type ImportErrorStore interface {
	ListImportErrors(ctx context.Context, tenant string) ([]domain.ImportError, error)
	SetImportError(ctx context.Context, tenant string, e domain.ImportError) error
	ClearImportError(ctx context.Context, tenant, filename string) error
}

// importErrorDTO is the Airflow 3.2.1 ImportErrorResponse. import_error_id is an
// integer in Airflow; Leoflow keys rows by UUID, so the DTO derives a stable
// integer id from the filename for the UI's list key.
type importErrorDTO struct {
	ImportErrorID uint32  `json:"import_error_id"`
	Timestamp     string  `json:"timestamp"`
	Filename      string  `json:"filename"`
	StackTrace    string  `json:"stack_trace"`
	BundleName    *string `json:"bundle_name"`
}

type importErrorCollectionDTO struct {
	ImportErrors []importErrorDTO `json:"import_errors"`
	TotalEntries int              `json:"total_entries"`
}

func toImportErrorDTO(e domain.ImportError) importErrorDTO {
	return importErrorDTO{
		ImportErrorID: crc32.ChecksumIEEE([]byte(e.Filename)),
		Timestamp:     e.Timestamp.UTC().Format(time.RFC3339),
		Filename:      e.Filename,
		StackTrace:    e.StackTrace,
		BundleName:    strPtrOrNil(e.BundleName),
	}
}

// importErrorBody is the push payload used by the dev watcher (Leoflow extension).
type importErrorBody struct {
	Filename   string `json:"filename"`
	StackTrace string `json:"stack_trace"`
	BundleName string `json:"bundle_name"`
}

func listImportErrorsHandler(store ImportErrorStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		errs, err := store.ListImportErrors(c.Request.Context(), tenantOf(c))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := importErrorCollectionDTO{ImportErrors: make([]importErrorDTO, 0, len(errs)), TotalEntries: len(errs)}
		for _, e := range errs {
			out.ImportErrors = append(out.ImportErrors, toImportErrorDTO(e))
		}
		c.JSON(http.StatusOK, out)
	}
}

func setImportErrorHandler(store ImportErrorStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body importErrorBody
		if err := c.ShouldBindJSON(&body); err != nil || body.Filename == "" {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "filename and stack_trace are required"})
			return
		}
		err := store.SetImportError(c.Request.Context(), tenantOf(c), domain.ImportError{
			Filename: body.Filename, StackTrace: body.StackTrace, BundleName: body.BundleName,
		})
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func clearImportErrorHandler(store ImportErrorStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		filename := c.Query("filename")
		if filename == "" {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "filename query parameter is required"})
			return
		}
		if err := store.ClearImportError(c.Request.Context(), tenantOf(c), filename); err != nil {
			handleRepoError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// registerImportErrors mounts the import-error feed. With no store it serves a
// schema-valid empty collection (the UI degrades gracefully). With a store, the
// public GET feed is real and the write verbs (Leoflow extensions, used by the
// dev watcher) upsert/clear entries by filename.
func registerImportErrors(r gin.IRouter, store ImportErrorStore) {
	if store == nil {
		r.GET("/api/v2/importErrors", apiEmptyCollection("import_errors"))
		return
	}
	r.GET("/api/v2/importErrors", listImportErrorsHandler(store))
	r.PUT("/api/v2/importErrors", RequirePermission("write", "dag"), setImportErrorHandler(store))
	r.DELETE("/api/v2/importErrors", RequirePermission("write", "dag"), clearImportErrorHandler(store))
}
