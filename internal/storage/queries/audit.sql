-- name: CreateAuditLog :exec
INSERT INTO audit_log (tenant_id, user_id, action, resource_type, resource_id)
VALUES ($1, $2, $3, $4, $5);
