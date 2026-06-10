package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/connectors"
)

// The connection-type catalog the SPA's "Add/Edit Connection" form reads from
// GET /ui/connections/hook_meta. Each entry tells the form which standard fields
// to render (host/login/password/port/schema/description, with per-field hide /
// relabel / placeholder behavior) and which provider-specific custom fields to
// render (extra_fields, the credential fields stored in Connection.extra). The
// metadata is generated from a real Airflow install (internal/connectors —
// catalog.json), so it is the exact shape the Airflow 3.2 SPA's FlexibleForm
// renders. Without this the edit form renders empty (the form is entirely driven
// by this metadata).

type hookMetaEntry struct {
	ConnectionType  string         `json:"connection_type"`
	HookName        string         `json:"hook_name"`
	HookClassName   string         `json:"hook_class_name"`
	DefaultConnName string         `json:"default_conn_name"`
	ExtraFields     map[string]any `json:"extra_fields"`
	StandardFields  map[string]any `json:"standard_fields"`
}

// sanitizeFields returns a copy with each nil-valued field spec replaced by a
// minimal visible spec ({"hidden": false}), and {} for a nil map. The Airflow 3.2
// FlexibleForm iterates each entry of standard_fields/extra_fields and reads
// `field.hidden`; the generated catalog emits e.g. "description": null for a
// standard field with no overrides, and reading `.hidden` off that null crashes
// the whole Add/Edit Connection page ("Cannot read properties of undefined
// (reading 'hidden')"). We keep the key (the form still renders the field) but
// give it a valid, visible default so the read never crashes.
func sanitizeFields(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if v == nil {
			out[k] = map[string]any{"hidden": false}
			continue
		}
		out[k] = v
	}
	return out
}

// connectionTypeCatalog builds the form catalog from the shared connector
// registry (internal/connectors — ADR 0038's single source of truth), serving
// each entry's generated standard_fields + extra_fields verbatim.
func connectionTypeCatalog() []hookMetaEntry {
	cat := connectors.Catalog()
	out := make([]hookMetaEntry, len(cat))
	for i, c := range cat {
		out[i] = hookMetaEntry{
			ConnectionType:  c.ConnectionType,
			HookName:        c.HookName,
			HookClassName:   c.HookClassName,
			DefaultConnName: c.DefaultConnName,
			ExtraFields:     sanitizeFields(c.ExtraFields),
			StandardFields:  sanitizeFields(c.StandardFields),
		}
	}
	return out
}

// connectionHookMetaHandler serves the connection-type catalog the form needs.
func connectionHookMetaHandler() gin.HandlerFunc {
	catalog := connectionTypeCatalog()
	return func(c *gin.Context) { c.JSON(http.StatusOK, catalog) }
}
