package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// supportedMenuItems are the Airflow 3.2.1 UI menu sections Leoflow backs. The
// UI renders only the sections /ui/auth/menus authorizes, so omitting the rest
// (Assets, Connections, Variables, Pools, Providers, Jobs, ...) hides them
// without modifying the SPA. See docs/ui-compatibility.md.
var supportedMenuItems = []string{"Dags", "XComs", "Docs", "Config"}

// registerUI mounts the Airflow 3.2.1 internal UI API (/ui/*) that the bundled
// React app calls. Unimplemented /ui paths degrade gracefully via uiNoRoute.
func registerUI(r gin.IRouter) {
	r.GET("/ui/config", uiConfigHandler())
	r.GET("/ui/auth/me", uiMeHandler())
	r.GET("/ui/auth/menus", uiMenusHandler())
}

// uiConfigHandler returns the UI ConfigResponse (Airflow 3.2.1 shape).
func uiConfigHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"fallback_page_limit":             50,
			"auto_refresh_interval":           3,
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

// uiNoRoute is the engine's NoRoute handler. For an unimplemented /ui path it
// degrades gracefully — an empty body for reads, a 501 hint for writes — so the
// UI shows an empty state or a toast instead of breaking. Other paths 404.
func uiNoRoute() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.HasPrefix(c.Request.URL.Path, "/ui/") {
			AbortProblem(c, http.StatusNotFound, "not found", "no such resource")
			return
		}
		if c.Request.Method == http.MethodGet {
			c.JSON(http.StatusOK, gin.H{})
			return
		}
		AbortProblem(c, http.StatusNotImplemented, "not implemented",
			"this action is not available in Leoflow yet")
	}
}
