package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/ui"
)

func uiServer() *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", Email: "admin@leoflow.local", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		UI:            ui.New(),
	})
}

func TestUIConfigInstanceNameConfigurable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := map[string]string{"": "Leoflow", "Leoflow · DEV": "Leoflow · DEV"}
	for in, want := range cases {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ui/config", http.NoBody)
		uiConfigHandler(in)(c)
		var cfg map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if cfg["instance_name"] != want {
			t.Errorf("instance_name for %q = %v, want %v", in, cfg["instance_name"], want)
		}
	}
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
	// Backed sections are advertised so the SPA renders them: Dags, Docs, the
	// Admin panel (Variables, Connections — #45) and Audit Log (#37).
	for _, want := range []string{"Dags", "Docs", "Variables", "Connections", "Audit Log"} {
		if !has(want) {
			t.Errorf("menus should authorize %q, got %v", want, body.Authorized)
		}
	}
	// Still-stubbed sections stay hidden.
	if has("Pools") || has("XComs") || has("Required Actions") || has("Providers") {
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
	// Unimplemented /ui read (no explicit stub) -> empty 200, not a 404 white
	// screen. /ui/gantt has no stub, so it exercises the NoRoute catch-all.
	if rec := authGet(srv, http.MethodGet, "/ui/gantt/etl/r1", ""); rec.Code != http.StatusOK || rec.Body.String() != "{}" {
		t.Errorf("unimplemented /ui GET = %d %q, want 200 {}", rec.Code, rec.Body.String())
	}
	// Unimplemented /ui write -> 501 hint.
	if rec := authGet(srv, http.MethodPost, "/ui/backfills", "{}"); rec.Code != http.StatusNotImplemented {
		t.Errorf("unimplemented /ui write = %d, want 501", rec.Code)
	}
	// Unmatched /api path -> 404 (never the SPA shell).
	if rec := authGet(srv, http.MethodGet, "/api/v2/bogus", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unmatched /api GET = %d, want 404", rec.Code)
	}
	// Non-/ui, non-/api GET -> SPA shell (client-side route), Airflow-style.
	rec := authGet(srv, http.MethodGet, "/dags/etl/grid", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<div id=\"root\"") {
		t.Errorf("client-side route = %d, want 200 SPA shell, got %q", rec.Code, rec.Body.String())
	}
	// Non-GET unknown -> 404.
	if rec := authGet(srv, http.MethodDelete, "/whatever", ""); rec.Code != http.StatusNotFound {
		t.Errorf("non-GET unknown = %d, want 404", rec.Code)
	}
}

func TestUnauthenticatedCanLoadSPAButNotData(t *testing.T) {
	srv := uiServer()
	anon := func(path string) int {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
		rec := httptest.NewRecorder() // no Authorization header
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	// The static SPA (shell + assets + pre-login config) must load anonymously,
	// or the browser can never reach the login screen.
	for _, p := range []string{"/", "/dags/etl/grid", "/static/VERSION", "/ui/config"} {
		if code := anon(p); code != http.StatusOK {
			t.Errorf("anonymous GET %s = %d, want 200 (public SPA)", p, code)
		}
	}
	// The data planes stay gated.
	for _, p := range []string{"/api/v2/version", "/ui/auth/me"} {
		if code := anon(p); code != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s = %d, want 401 (gated data)", p, code)
		}
	}
}

func TestUIServesStaticAndIndexShell(t *testing.T) {
	srv := uiServer()
	// Root serves the SPA shell with the templated base href.
	rec := authGet(srv, http.MethodGet, "/", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<base href=\"/\"") {
		t.Errorf("root shell = %d, body=%q", rec.Code, rec.Body.String())
	}
	// The embedded bundle (placeholder or fetched) serves files under /static.
	// (index.html itself is 301-canonicalized to ./ by http.FileServer, so we
	// probe the VERSION marker, which every bundle carries.)
	rec = authGet(srv, http.MethodGet, "/static/VERSION", "")
	if rec.Code != http.StatusOK {
		t.Errorf("/static/VERSION = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc == "" {
		t.Errorf("/static response missing Cache-Control")
	}
}
