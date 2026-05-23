package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// supportedMenuItems are the Airflow 3.2.1 UI menu sections Leoflow backs. The
// UI renders only the sections /ui/auth/menus authorizes, so omitting the rest
// (Assets, Pools, Providers, Jobs, XComs, ...) hides them without modifying the
// SPA. Each value must be a real 3.2.1 MenuItem enum member (validMenuItems).
// The set is widened as sections gain real backing: Variables and Connections
// (the Admin panel, #45) and Audit Log (Browse, #37) are implemented, so they
// are advertised; still-stubbed sections stay hidden. See docs/ui-compatibility.md.
var supportedMenuItems = []string{"Dags", "Variables", "Connections", "Audit Log", "Docs"}

// validMenuItems is the Airflow 3.2.1 MenuItem string enum. /ui/auth/menus may
// only advertise values from this set; the SPA ignores unknown items.
var validMenuItems = map[string]bool{
	"Required Actions": true, "Assets": true, "Audit Log": true, "Config": true,
	"Connections": true, "Dags": true, "Docs": true, "Jobs": true,
	"Plugins": true, "Pools": true, "Providers": true, "Variables": true,
	"XComs": true,
}

// uiConfigRequiredFields mirrors the fields the Airflow 3.2.1 ConfigResponse
// schema marks required. The UI may silently misrender if any is absent, so the
// /ui/config payload must always carry every one. The browser E2E is the real
// validation; this list is the cheap unit-test guard. See docs/ui-compatibility.md.
var uiConfigRequiredFields = []string{
	"fallback_page_limit", "auto_refresh_interval", "hide_paused_dags_by_default",
	"instance_name", "enable_swagger_ui", "require_confirmation_dag_change",
	"default_wrap", "test_connection", "dashboard_alert", "show_external_log_redirect",
	"theme", "multi_team",
}

// registerUI mounts the Airflow 3.2.1 internal UI API (/ui/*) that the bundled
// React app calls. Unimplemented /ui paths degrade gracefully via uiNoRoute.
// tokenTTLSecs feeds the expires_in_seconds field of /ui/auth/token.
func registerUI(r gin.IRouter, tokenTTLSecs int) {
	r.GET("/ui/config", uiConfigHandler())
	r.GET("/ui/auth/me", uiMeHandler())
	r.GET("/ui/auth/menus", uiMenusHandler())
	r.POST("/ui/auth/token", uiTokenHandler(tokenTTLSecs))
}

// uiConfigHandler returns the UI ConfigResponse (Airflow 3.2.1 shape). It keeps
// every spec-required field present; values stay minimal for the MVP (Phase 5.3
// tunes them). theme is null — required-but-nullable in the spec — meaning "no
// custom Chakra theme". is_db_isolation_mode is intentionally absent: it is not
// part of the 3.2.1 ConfigResponse.
func uiConfigHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"fallback_page_limit":             50,
			"auto_refresh_interval":           30,
			"hide_paused_dags_by_default":     false,
			"instance_name":                   "Leoflow",
			"enable_swagger_ui":               true,
			"require_confirmation_dag_change": false,
			"default_wrap":                    false,
			"test_connection":                 "Disabled",
			"dashboard_alert":                 []any{},
			"show_external_log_redirect":      false,
			"external_log_name":               nil,
			"theme":                           nil,
			"multi_team":                      false,
		})
	}
}

// uiTokenHandler implements POST /ui/auth/token: it re-mints a bearer token for
// an already-authenticated principal. Per the 3.2.1 spec the body carries no
// credentials (only an optional token_type), so this is NOT the login endpoint
// — credential login is the simple-auth-manager POST /auth/token. Without a
// valid bearer it returns 401. The response is GenerateTokenResponse
// (access_token, token_type, expires_in_seconds). See docs/ui-compatibility.md.
func uiTokenHandler(tokenTTLSecs int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := UserFromContext(c); !ok {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "no authenticated user")
			return
		}
		token := bearerToken(c.GetHeader("Authorization"))
		if token == "" {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "no bearer token")
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"access_token":       token,
			"token_type":         "bearer",
			"expires_in_seconds": tokenTTLSecs,
		})
	}
}

// uiMeHandler returns the authenticated user (AuthenticatedMeResponse).
func uiMeHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := UserFromContext(c)
		if !ok {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "no authenticated user")
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": user.ID, "username": user.Email})
	}
}

// uiMenusHandler returns only the menu sections Leoflow backs, so the UI hides
// the rest (MenuItemCollectionResponse).
func uiMenusHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"authorized_menu_items": supportedMenuItems,
			"extra_menu_items":      []any{},
		})
	}
}

// uiNoRoute is the engine's NoRoute handler, mirroring Airflow's catch-all. An
// unimplemented /ui path degrades gracefully (empty body for reads, 501 hint for
// writes). An unmatched /api path is a 404. Any other GET falls back to the SPA
// shell so the React router can handle client-side routes; without a UI server,
// or for non-GET, it is a 404.
func uiNoRoute(uiSrv UIServer) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		switch {
		case strings.HasPrefix(path, "/ui/"):
			if c.Request.Method == http.MethodGet {
				c.JSON(http.StatusOK, gin.H{})
				return
			}
			AbortProblem(c, http.StatusNotImplemented, "not implemented",
				"this action is not available in Leoflow yet")
			return
		case strings.HasPrefix(path, "/api/"):
			AbortProblem(c, http.StatusNotFound, "not found", "API route not found")
			return
		}
		if uiSrv != nil && c.Request.Method == http.MethodGet {
			uiSrv.Index(c.Writer, "/")
			return
		}
		AbortProblem(c, http.StatusNotFound, "not found", "no such resource")
	}
}
