-- name: CreateAuditLog :exec
INSERT INTO audit_log (tenant_id, user_id, action, resource_type, resource_id, metadata)
VALUES ($1, $2, $3, $4, $5, COALESCE(sqlc.narg('metadata')::jsonb, '{}'::jsonb));

-- name: ListAuditLogs :many
SELECT a.id, a.action, a.resource_type, a.resource_id, a.metadata, a.occurred_at,
       COALESCE(u.email, '') AS owner
FROM audit_log a
LEFT JOIN users u ON u.id = a.user_id
WHERE a.tenant_id = $1
  AND (sqlc.narg('dag_id')::text IS NULL OR a.resource_id = sqlc.narg('dag_id'))
ORDER BY a.occurred_at DESC, a.id DESC
LIMIT $2 OFFSET $3;

-- name: CountAuditLogs :one
SELECT count(*)
FROM audit_log a
WHERE a.tenant_id = $1
  AND (sqlc.narg('dag_id')::text IS NULL OR a.resource_id = sqlc.narg('dag_id'));
