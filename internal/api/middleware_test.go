package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
)

// tokenAuthn authenticates exactly one token value; anything else is invalid. It
// lets a test distinguish a stale bearer from a valid session cookie.
type tokenAuthn struct{ valid string }

func (tokenAuthn) IssueToken(context.Context, auth.Credentials) (string, error) { return "", nil }
func (a tokenAuthn) Authenticate(_ context.Context, token string) (*auth.User, error) {
	if token == a.valid {
		return &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, nil
	}
	return nil, auth.ErrInvalidToken
}

// TestJWTAuthFallsBackToCookieWhenBearerInvalid covers the SPA-refresh login bug:
// the Airflow UI loses its in-memory token on refresh and may send a stale/empty
// bearer while a valid session lives in the _token cookie. JWTAuth must try the
// bearer AND fall back to the cookie, accepting whichever validates — otherwise an
// invalid bearer short-circuits to 401 and the UI bounces to login ("flash").
func TestJWTAuthFallsBackToCookieWhenBearerInvalid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	run := func(setup func(*http.Request)) int {
		r := gin.New()
		r.Use(JWTAuth(tokenAuthn{valid: "good"}))
		r.GET("/ui/auth/me", func(c *gin.Context) { c.Status(http.StatusOK) })
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ui/auth/me", http.NoBody)
		setup(req)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	cases := []struct {
		name  string
		setup func(*http.Request)
		want  int
	}{
		{"stale bearer + valid cookie -> cookie wins", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer stale-or-undefined")
			r.AddCookie(&http.Cookie{Name: authTokenCookie, Value: "good"})
		}, http.StatusOK},
		{"valid bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer good") }, http.StatusOK},
		{"valid cookie only", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: authTokenCookie, Value: "good"}) }, http.StatusOK},
		{"invalid bearer, no cookie", func(r *http.Request) { r.Header.Set("Authorization", "Bearer bad") }, http.StatusUnauthorized},
		{"nothing", func(*http.Request) {}, http.StatusUnauthorized},
	}
	for _, c := range cases {
		if got := run(c.setup); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

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
