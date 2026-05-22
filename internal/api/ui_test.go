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
	// Every field the spec marks required must be present, or the SPA may
	// silently misrender. See uiConfigRequiredFields (mirrored from the spec).
	for _, field := range uiConfigRequiredFields {
		if _, ok := cfg[field]; !ok {
			t.Errorf("config missing required field %q", field)
		}
	}
	if cfg["instance_name"] != "Leoflow" {
		t.Errorf("instance_name = %v, want Leoflow", cfg["instance_name"])
	}
	if cfg["auto_refresh_interval"].(float64) != 30 {
		t.Errorf("auto_refresh_interval = %v, want 30", cfg["auto_refresh_interval"])
	}
	// theme is required AND nullable in the spec (Theme object | null); null
	// means "no custom Chakra theme". It is NOT the string "default".
	if v, ok := cfg["theme"]; ok && v != nil {
		t.Errorf("theme = %v, want null", v)
	}
	// is_db_isolation_mode is not part of the 3.2.1 ConfigResponse.
	if _, ok := cfg["is_db_isolation_mode"]; ok {
		t.Errorf("config carries is_db_isolation_mode, not in spec")
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
	if !has("Dags") || !has("Docs") {
		t.Errorf("menus should authorize Dags and Docs, got %v", body.Authorized)
	}
	if has("Connections") || has("Variables") || has("Pools") || has("XComs") || has("Required Actions") {
		t.Errorf("menus must hide unsupported sections, got %v", body.Authorized)
	}
	// Every advertised item must be a real 3.2.1 MenuItem enum value.
	for _, m := range body.Authorized {
		if !validMenuItems[m] {
			t.Errorf("menu item %q is not a 3.2.1 MenuItem enum value", m)
		}
	}
}

func TestUIAuthTokenReMintsForAuthenticatedPrincipal(t *testing.T) {
	srv := uiServer()
	// Authenticated principal: re-mint succeeds with the spec response shape.
	rec := authGet(srv, http.MethodPost, "/ui/auth/token", `{"token_type":"api"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/auth/token = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var tok map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"access_token", "token_type", "expires_in_seconds"} {
		if _, ok := tok[f]; !ok {
			t.Errorf("token response missing %q (got %v)", f, tok)
		}
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
