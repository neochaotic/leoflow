-- name: UpsertDag :one
INSERT INTO dags (tenant_id, dag_id, description, owner, schedule, schedule_timezone, max_active_runs, catchup)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (tenant_id, dag_id) DO UPDATE
SET description = EXCLUDED.description,
    owner = EXCLUDED.owner,
    schedule = EXCLUDED.schedule,
    schedule_timezone = EXCLUDED.schedule_timezone,
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
