-- name: CountUsers :one
SELECT count(*) FROM users WHERE tenant_id = $1;

-- name: CreateUser :one
INSERT INTO users (tenant_id, email, password_hash)
VALUES ($1, $2, $3)
RETURNING id;

-- name: GetRoleByName :one
SELECT id FROM roles WHERE tenant_id = $1 AND name = $2;

-- name: AssignUserRole :exec
INSERT INTO user_roles (user_id, role_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;
