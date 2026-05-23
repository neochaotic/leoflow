-- name: UpsertDag :one
INSERT INTO dags (tenant_id, dag_id, description, owner, tags, schedule, schedule_timezone, start_date, max_active_runs, catchup)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (tenant_id, dag_id) DO UPDATE
SET description = EXCLUDED.description,
    owner = EXCLUDED.owner,
    tags = EXCLUDED.tags,
    schedule = EXCLUDED.schedule,
    schedule_timezone = EXCLUDED.schedule_timezone,
    start_date = EXCLUDED.start_date,
    max_active_runs = EXCLUDED.max_active_runs,
    catchup = EXCLUDED.catchup,
    updated_at = now()
RETURNING *;

-- name: GetDagByDagID :one
SELECT * FROM dags WHERE tenant_id = $1 AND dag_id = $2;

-- name: ListDags :many
SELECT * FROM dags
WHERE tenant_id = $1 AND is_active = true
ORDER BY dag_id
LIMIT $2 OFFSET $3;

-- name: CountDags :one
SELECT count(*) FROM dags WHERE tenant_id = $1 AND is_active = true;

-- name: SetDagPaused :one
UPDATE dags SET is_paused = $3, updated_at = now()
WHERE tenant_id = $1 AND dag_id = $2
RETURNING *;

-- name: GetDagVersionByHash :one
SELECT * FROM dag_versions WHERE dag_id = $1 AND spec_hash = $2;

-- name: InsertDagVersion :one
INSERT INTO dag_versions (dag_id, version, image_reference, spec, spec_hash, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: SetCurrentDagVersion :exec
UPDATE dags SET current_version_id = $2, updated_at = now() WHERE id = $1;

-- name: GetCurrentDagSpec :one
SELECT v.spec
FROM dags d
JOIN dag_versions v ON v.id = d.current_version_id
WHERE d.tenant_id = $1 AND d.dag_id = $2;

-- name: ListDagVersions :many
SELECT v.id, v.version, v.created_at,
       row_number() OVER (ORDER BY v.created_at, v.version) AS version_number
FROM dag_versions v
JOIN dags d ON d.id = v.dag_id
WHERE d.tenant_id = $1 AND d.dag_id = $2
ORDER BY version_number DESC;

-- name: DeleteDag :execrows
DELETE FROM dags
WHERE tenant_id = $1 AND dag_id = $2;

-- name: ListDagsFiltered :many
WITH latest AS (
    SELECT DISTINCT ON (r.dag_id) r.dag_id, r.state
    FROM dag_runs r
    ORDER BY r.dag_id, r.logical_date DESC
)
SELECT d.*
FROM dags d
LEFT JOIN latest l ON l.dag_id = d.id
WHERE d.tenant_id = $1 AND d.is_active = true
  AND (sqlc.narg('paused')::bool IS NULL OR d.is_paused = sqlc.narg('paused'))
  AND (sqlc.narg('run_state')::dag_run_state IS NULL OR l.state = sqlc.narg('run_state'))
ORDER BY d.dag_id
LIMIT $2 OFFSET $3;

-- name: CountDagsFiltered :one
WITH latest AS (
    SELECT DISTINCT ON (r.dag_id) r.dag_id, r.state
    FROM dag_runs r
    ORDER BY r.dag_id, r.logical_date DESC
)
SELECT count(*)
FROM dags d
LEFT JOIN latest l ON l.dag_id = d.id
WHERE d.tenant_id = $1 AND d.is_active = true
  AND (sqlc.narg('paused')::bool IS NULL OR d.is_paused = sqlc.narg('paused'))
  AND (sqlc.narg('run_state')::dag_run_state IS NULL OR l.state = sqlc.narg('run_state'));

-- name: ClearDagRuns :execrows
DELETE FROM dag_runs WHERE dag_id = $1;
