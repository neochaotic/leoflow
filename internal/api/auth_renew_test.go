package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
)

// fakeRenewer records the renewal request and returns a canned result, so the
// handler's status mapping can be exercised without a real signing key.
type fakeRenewer struct {
	renewed string
	ok      bool
	err     error

	gotToken string
	gotTTL   time.Duration
	gotMax   time.Duration
}

func (f *fakeRenewer) RenewUserToken(token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error) {
	f.gotToken, f.gotTTL, f.gotMax = token, ttl, maxLifetime
	return f.renewed, f.ok, f.err
}

func renewServer(r TokenRenewer) *gin.Engine {
	return NewServer(Dependencies{
		Logger:               discardLogger(),
		Authenticator:        &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:          auth.NewRateLimiter(5, time.Minute),
		HealthChecks:         map[string]HealthChecker{},
		CORSOrigins:          []string{"*"},
		TokenTTLSecs:         3600,
		TokenRenewer:         r,
		TokenMaxLifetimeSecs: 86400,
	})
}

func postRenew(srv *gin.Engine, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v2/auth/token/renew", http.NoBody)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestRenewTokenReturnsFreshToken: a valid bearer is renewed into a fresh access
// token, returned in the same shape as /auth/token, and the handler passes the
// configured TTL and max_lifetime to the renewer along with the caller's bearer.
func TestRenewTokenReturnsFreshToken(t *testing.T) {
	r := &fakeRenewer{renewed: "new-token", ok: true}
	rec := postRenew(renewServer(r), "current-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("renew = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.AccessToken != "new-token" || resp.TokenType != "bearer" || resp.ExpiresIn != 3600 {
		t.Errorf("response = %+v, want access_token=new-token bearer expires_in=3600", resp)
	}
	if r.gotToken != "current-token" {
		t.Errorf("renewer got token %q, want the caller's bearer", r.gotToken)
	}
	if r.gotTTL != time.Hour {
		t.Errorf("renewer TTL = %v, want 1h (TokenTTLSecs)", r.gotTTL)
	}
	if r.gotMax != 24*time.Hour {
		t.Errorf("renewer max_lifetime = %v, want 24h (TokenMaxLifetimeSecs)", r.gotMax)
	}
}

// TestRenewTokenInvalidIsUnauthorized: when the renewer rejects the token (bad
// signature / expired), the handler answers 401 so the CLI falls back to login.
func TestRenewTokenInvalidIsUnauthorized(t *testing.T) {
	r := &fakeRenewer{err: auth.ErrInvalidToken}
	rec := postRenew(renewServer(r), "expired-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("renew of invalid token = %d, want 401", rec.Code)
	}
}

// TestRenewTokenPastMaxLifetimeIsUnauthorized: a token past the session
// max_lifetime (renewer returns ok=false, no error) is refused with 401 — the
// user must re-authenticate.
func TestRenewTokenPastMaxLifetimeIsUnauthorized(t *testing.T) {
	r := &fakeRenewer{ok: false}
	rec := postRenew(renewServer(r), "aged-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("renew past max_lifetime = %d, want 401", rec.Code)
	}
}
