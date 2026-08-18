-- name: GetDefaultTenant :one
SELECT id, name FROM tenants WHERE name = 'default';

-- name: GetTenantByName :one
SELECT id, name FROM tenants WHERE name = $1;

-- name: InsertUser :one
-- Mirrors the bootstrap CreateUser insert (tenant_id, email, password_hash) but
-- returns the columns the admin create-user API echoes back to the caller.
INSERT INTO users (tenant_id, email, password_hash)
VALUES ($1, $2, $3)
RETURNING id, email, is_active, created_at;

-- name: GetUserByEmail :one
SELECT u.id, u.tenant_id, u.email, u.password_hash, u.is_active
FROM users u
JOIN tenants t ON t.id = u.tenant_id
WHERE t.name = $1 AND u.email = $2;

-- name: UpdateUserPassword :execrows
UPDATE users
SET password_hash = $3
WHERE email = $2
  AND tenant_id = (SELECT id FROM tenants WHERE name = $1);

-- name: GetUserRoles :many
SELECT r.name
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
WHERE ur.user_id = $1;

-- name: GetUserPermissions :many
SELECT DISTINCT p.action, p.resource
FROM user_roles ur
JOIN role_permissions rp ON rp.role_id = ur.role_id
JOIN permissions p ON p.id = rp.permission_id
WHERE ur.user_id = $1;
