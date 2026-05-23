-- name: ListVariables :many
SELECT key, value, description
FROM variables
WHERE tenant_id = $1
ORDER BY key
LIMIT $2 OFFSET $3;

-- name: CountVariables :one
SELECT count(*) FROM variables WHERE tenant_id = $1;

-- name: GetVariable :one
SELECT key, value, description
FROM variables
WHERE tenant_id = $1 AND key = $2;

-- name: UpsertVariable :exec
INSERT INTO variables (tenant_id, key, value, description)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id, key) DO UPDATE SET
    value = EXCLUDED.value,
    description = EXCLUDED.description,
    updated_at = now();

-- name: DeleteVariable :execrows
DELETE FROM variables WHERE tenant_id = $1 AND key = $2;
