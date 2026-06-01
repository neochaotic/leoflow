package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestNoStoreOnVolatileRoutes pins the rule that the SPA-facing JSON surface
// (every `/api/v2/*` and `/ui/*` response) must declare itself uncacheable.
// Without this header the browser HTTP cache returns the pre-mutation
// response after a mark-state PATCH, so the user sees stale state even
// though TanStack Query already invalidated its in-memory cache (#211, #271).
//
// Static assets the SPA loads from /ide/vs/... (Monaco bundle) are
// content-hashed and SHOULD be cached, so the middleware skips them.
func TestNoStoreOnVolatileRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(NoStoreOnVolatileRoutes())
	r.GET("/api/v2/dags", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	r.GET("/ui/dags", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	r.PATCH("/api/v2/dags/:id/dagRuns/:r/taskInstances/:t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{})
	})
	r.GET("/ide/vs/loader.js", func(c *gin.Context) { c.String(http.StatusOK, "// monaco") })
	r.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })

	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), method, path, http.NoBody))
		return rec
	}

	cases := []struct {
		name          string
		method        string
		path          string
		wantNoStore   bool
		wantStatus    int
		wantSetHeader string
	}{
		{"v2 dags list", http.MethodGet, "/api/v2/dags", true, http.StatusOK, "no-store, must-revalidate"},
		{"ui dags helper", http.MethodGet, "/ui/dags", true, http.StatusOK, "no-store, must-revalidate"},
		{"v2 mark-state PATCH", http.MethodPatch, "/api/v2/dags/d/dagRuns/r/taskInstances/t", true, http.StatusOK, "no-store, must-revalidate"},
		{"monaco asset", http.MethodGet, "/ide/vs/loader.js", false, http.StatusOK, ""},
		{"healthz", http.MethodGet, "/healthz", false, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(tc.method, tc.path)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			got := rec.Header().Get("Cache-Control")
			if tc.wantNoStore {
				if got != tc.wantSetHeader {
					t.Errorf("Cache-Control = %q, want %q", got, tc.wantSetHeader)
				}
			} else {
				if got != "" {
					t.Errorf("Cache-Control = %q on non-volatile route; want unset", got)
				}
			}
		})
	}
}
