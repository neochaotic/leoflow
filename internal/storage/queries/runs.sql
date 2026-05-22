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

-- name: ResetTaskInstanceToNone :exec
UPDATE task_instances
SET state = 'none', started_at = NULL, ended_at = NULL, try_number = try_number + 1
WHERE dag_run_id = $1 AND task_id = $2;
