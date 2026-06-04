package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/connectors"
)

// The connection-type catalog the SPA's "Add/Edit Connection" form reads from
// GET /ui/connections/hook_meta. Each entry tells the form which standard fields
// to render (host/login/password/port/schema/description) and the type's display
// name — borrowed from Airflow's providers so the common connections work and the
// user just edits them. Without this the edit form renders empty (the form is
// entirely driven by this metadata).
//
// standard_fields keys mirror what the SPA consumes: description, host, login,
// password, port, and url_schema (the "Schema" field). Each value is a behavior
// object; { "hidden": true } drops the field for that type.

type hookMetaEntry struct {
	ConnectionType  string         `json:"connection_type"`
	HookName        string         `json:"hook_name"`
	HookClassName   string         `json:"hook_class_name"`
	DefaultConnName string         `json:"default_conn_name"`
	ExtraFields     map[string]any `json:"extra_fields"`
	StandardFields  map[string]any `json:"standard_fields"`
}

// stdFields builds the standard-field behavior map, hiding the named fields.
func stdFields(hidden ...string) map[string]any {
	h := make(map[string]bool, len(hidden))
	for _, f := range hidden {
		h[f] = true
	}
	out := make(map[string]any, 6)
	for _, f := range []string{"description", "host", "login", "password", "port", "url_schema"} {
		out[f] = map[string]any{"hidden": h[f]}
	}
	return out
}

// connectionTypeCatalog builds the form catalog from the shared connector
// registry (internal/connectors — ADR 0038's single source of truth), adapting
// each entry to the form DTO (the standard-field hidden behavior the SPA reads).
func connectionTypeCatalog() []hookMetaEntry {
	cat := connectors.Catalog()
	out := make([]hookMetaEntry, len(cat))
	for i, c := range cat {
		out[i] = hookMetaEntry{
			ConnectionType:  c.Type,
			HookName:        c.DisplayName,
			HookClassName:   c.HookClass,
			DefaultConnName: c.DefaultConnName,
			ExtraFields:     map[string]any{},
			StandardFields:  stdFields(c.HiddenFields...),
		}
	}
	return out
}

// connectionHookMetaHandler serves the connection-type catalog the form needs.
func connectionHookMetaHandler() gin.HandlerFunc {
	catalog := connectionTypeCatalog()
	return func(c *gin.Context) { c.JSON(http.StatusOK, catalog) }
}
