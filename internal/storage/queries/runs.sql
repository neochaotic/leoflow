-- name: CreateDagRun :one
INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger, note)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetDagRun :one
SELECT * FROM dag_runs WHERE dag_id = $1 AND run_id = $2;

-- name: ListDagRunsByDag :many
SELECT * FROM dag_runs
WHERE dag_id = $1
ORDER BY logical_date DESC
LIMIT $2 OFFSET $3;

-- name: CountDagRunsByDag :one
SELECT count(*) FROM dag_runs WHERE dag_id = $1;

-- name: ListActiveDagRuns :many
SELECT * FROM dag_runs
WHERE state IN ('queued', 'running')
ORDER BY queued_at;

-- name: UpdateDagRunState :one
UPDATE dag_runs
SET state = $2, started_at = $3, ended_at = $4
WHERE id = $1
RETURNING *;

-- name: CreateTaskInstance :one
INSERT INTO task_instances (tenant_id, dag_run_id, task_id, operator, max_tries, state)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListTaskInstancesByRun :many
SELECT * FROM task_instances
WHERE dag_run_id = $1
ORDER BY task_id;

-- name: UpdateTaskInstanceState :one
UPDATE task_instances
SET state = $2, started_at = $3, ended_at = $4
WHERE id = $1
RETURNING *;

-- name: ListScheduledDags :many
SELECT d.dag_id, d.schedule,
  (SELECT max(dr.logical_date) FROM dag_runs dr WHERE dr.dag_id = d.id) AS last_logical
FROM dags d
WHERE d.is_active = true AND d.is_paused = false
  AND d.schedule IS NOT NULL AND d.current_version_id IS NOT NULL;

-- name: CreateScheduledRunByDagID :exec
INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger)
SELECT d.tenant_id, d.id, d.current_version_id, sqlc.arg(run_id), sqlc.arg(logical_date), 'queued', 'scheduled'
FROM dags d
JOIN tenants t ON t.id = d.tenant_id
WHERE t.name = sqlc.arg(tenant) AND d.dag_id = sqlc.arg(dag_id) AND d.current_version_id IS NOT NULL
ON CONFLICT (dag_id, run_id) DO NOTHING;

-- name: GetDagVersionByID :one
SELECT * FROM dag_versions WHERE id = $1;

-- name: GetDagRunByID :one
SELECT * FROM dag_runs WHERE id = $1;

-- name: UpdateTaskInstanceStateByRunTask :exec
UPDATE task_instances SET state = $3
WHERE dag_run_id = $1 AND task_id = $2;

-- name: ResetTaskInstanceToNone :exec
UPDATE task_instances
SET state = 'none', started_at = NULL, ended_at = NULL, try_number = try_number + 1
WHERE dag_run_id = $1 AND task_id = $2;

-- name: FailTaskInstanceIfActive :exec
UPDATE task_instances
SET state = 'failed', ended_at = now(), error_message = $2
WHERE id = $1 AND state IN ('scheduled', 'queued', 'running');

-- name: ReportTaskResult :exec
UPDATE task_instances
SET state = $3,
    exit_code = $4,
    error_message = $5,
    started_at = CASE WHEN $3 = 'running' AND started_at IS NULL THEN now() ELSE started_at END,
    ended_at = CASE WHEN $3 IN ('success', 'failed', 'skipped', 'upstream_failed') THEN now() ELSE ended_at END,
    duration_seconds = CASE WHEN $3 IN ('success', 'failed') AND started_at IS NOT NULL
        THEN EXTRACT(EPOCH FROM (now() - started_at)) ELSE duration_seconds END
WHERE dag_run_id = $1 AND task_id = $2;
