package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
)

func uiServer() *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", Email: "admin@leoflow.local", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
	})
}

func TestUIConfigIsPublicAndShaped(t *testing.T) {
	rec := authGet(uiServer(), http.MethodGet, "/ui/config", "") // no token: must still work (public)
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/config = %d, want 200", rec.Code)
	}
	var cfg map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"instance_name", "auto_refresh_interval", "hide_paused_dags_by_default"} {
		if _, ok := cfg[field]; !ok {
			t.Errorf("config missing %q", field)
		}
	}
}

func TestUIMenusHidesUnsupportedSections(t *testing.T) {
	rec := authGet(uiServer(), http.MethodGet, "/ui/auth/menus", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/auth/menus = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Authorized []string `json:"authorized_menu_items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	has := func(s string) bool {
		for _, m := range body.Authorized {
			if m == s {
				return true
			}
		}
		return false
	}
	if !has("Dags") {
		t.Errorf("menus should authorize Dags, got %v", body.Authorized)
	}
	if has("Connections") || has("Variables") || has("Pools") {
		t.Errorf("menus must hide unsupported sections, got %v", body.Authorized)
	}
}

func TestUIMeReturnsUser(t *testing.T) {
	rec := authGet(uiServer(), http.MethodGet, "/ui/auth/me", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/auth/me = %d, want 200", rec.Code)
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.Username != "admin@leoflow.local" {
		t.Errorf("username = %q", me.Username)
	}
}

func TestUINoRouteDegradesGracefully(t *testing.T) {
	srv := uiServer()
	// Unimplemented /ui read -> empty 200, not a 404 white screen.
	if rec := authGet(srv, http.MethodGet, "/ui/calendar/etl", ""); rec.Code != http.StatusOK || rec.Body.String() != "{}" {
		t.Errorf("unimplemented /ui GET = %d %q, want 200 {}", rec.Code, rec.Body.String())
	}
	// Unimplemented /ui write -> 501 hint.
	if rec := authGet(srv, http.MethodPost, "/ui/backfills", "{}"); rec.Code != http.StatusNotImplemented {
		t.Errorf("unimplemented /ui write = %d, want 501", rec.Code)
	}
	// Non-/ui unknown path -> 404.
	if rec := authGet(srv, http.MethodGet, "/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404", rec.Code)
	}
}
