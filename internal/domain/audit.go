package domain

import "time"

// AuditLogEntry is one recorded action against a resource — the source for the
// UI's Audit Log table. ResourceID carries the DAG id for dag-scoped events.
type AuditLogEntry struct {
	ID           int64
	When         time.Time
	Action       string
	ResourceType string
	ResourceID   string
	Owner        string
	Extra        string
}
