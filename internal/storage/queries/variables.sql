-- name: ListVariables :many
SELECT key, value, description
FROM variables
WHERE tenant_id = $1
ORDER BY key
LIMIT $2 OFFSET $3;

-- name: ListVariablesScoped :many
-- The tenant's variables restricted to the given keys, filtered in the query
-- (ADR 0055 D1: scope in the SQL, never post-filter the decrypted whole vault in
-- the handler). Used under secret_scoping: enforce so a task receives only the
-- Variables it declared. An empty key set never reaches here — the handler
-- returns nothing without a query.
SELECT key, value, description
FROM variables
WHERE tenant_id = $1 AND key = ANY(sqlc.arg(keys)::text[])
ORDER BY key;

-- name: CountVariables :one
SELECT count(*) FROM variables WHERE tenant_id = $1;

-- name: GetVariable :one
SELECT key, value, description
FROM variables
WHERE tenant_id = $1 AND key = $2;

-- name: ExistingVariableKeys :many
-- The subset of the given keys that exist for the tenant. Used to reject a DAG
-- that declares an unknown Variable at registration (ADR 0055 D6); a name absent
-- from the result does not exist.
SELECT key FROM variables
WHERE tenant_id = $1 AND key = ANY(sqlc.arg(keys)::text[]);

-- name: UpsertVariable :exec
INSERT INTO variables (tenant_id, key, value, description)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id, key) DO UPDATE SET
    value = EXCLUDED.value,
    description = EXCLUDED.description,
    updated_at = now();

-- name: DeleteVariable :execrows
DELETE FROM variables WHERE tenant_id = $1 AND key = $2;
