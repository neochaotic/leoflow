-- name: ListConnections :many
SELECT conn_id, conn_type, host, conn_schema, login, port, extra, description
FROM connections
WHERE tenant_id = $1
ORDER BY conn_id
LIMIT $2 OFFSET $3;

-- name: CountConnections :one
SELECT count(*) FROM connections WHERE tenant_id = $1;

-- name: ListConnectionSecrets :many
-- All of a tenant's connections WITH the encrypted password, for delivering
-- credentials to task pods (ADR 0021). Never use this for UI/API responses,
-- which must mask the password.
SELECT conn_id, conn_type, host, conn_schema, login, password, port, extra
FROM connections
WHERE tenant_id = $1
ORDER BY conn_id;

-- name: ListConnectionSecretsScoped :many
-- The tenant's connections WITH the encrypted password, restricted to the given
-- conn_ids and filtered in the query (ADR 0055 D1: scope in the SQL, never
-- post-filter the decrypted whole vault). Never use this for UI/API responses,
-- which must mask the password. Used under secret_scoping: enforce so a task
-- receives only the connections it declared. An empty conn_id set never reaches
-- here — the handler returns nothing without a query.
SELECT conn_id, conn_type, host, conn_schema, login, password, port, extra
FROM connections
WHERE tenant_id = $1 AND conn_id = ANY(sqlc.arg(conn_ids)::text[])
ORDER BY conn_id;

-- name: GetConnection :one
SELECT conn_id, conn_type, host, conn_schema, login, password, port, extra, description
FROM connections
WHERE tenant_id = $1 AND conn_id = $2;

-- name: ExistingConnectionIDs :many
-- The subset of the given conn_ids that exist for the tenant. Used to reject a
-- DAG that declares an unknown connection at registration (ADR 0055 D6); a name
-- absent from the result does not exist.
SELECT conn_id FROM connections
WHERE tenant_id = $1 AND conn_id = ANY(sqlc.arg(conn_ids)::text[]);

-- name: UpsertConnection :exec
INSERT INTO connections (tenant_id, conn_id, conn_type, host, conn_schema, login, password, port, extra, description)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (tenant_id, conn_id) DO UPDATE SET
    conn_type = EXCLUDED.conn_type,
    host = EXCLUDED.host,
    conn_schema = EXCLUDED.conn_schema,
    login = EXCLUDED.login,
    password = EXCLUDED.password,
    port = EXCLUDED.port,
    extra = EXCLUDED.extra,
    description = EXCLUDED.description,
    updated_at = now();

-- name: DeleteConnection :execrows
DELETE FROM connections WHERE tenant_id = $1 AND conn_id = $2;
