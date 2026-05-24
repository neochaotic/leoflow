package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestDevBypassAuthInjectsAdmin verifies the dev-only auth bypass: a protected
// data-plane route is reachable with NO token, and the request carries an admin
// user (so RBAC checks pass). This middleware must only ever be wired under the
// explicit dev opt-in.
func TestDevBypassAuthInjectsAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(DevBypassAuth())
	r.GET("/api/v2/protected", func(c *gin.Context) {
		u, ok := UserFromContext(c)
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{"email": u.Email, "roles": u.Roles})
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/protected", http.NoBody)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no token needed under dev bypass)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "admin") {
		t.Errorf("expected an admin user, got %s", rec.Body.String())
	}
}
