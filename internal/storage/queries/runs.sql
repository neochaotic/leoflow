-- name: CreateDagRun :one
INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger, note)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetDagRun :one
SELECT * FROM dag_runs WHERE dag_id = $1 AND run_id = $2;

-- name: DeleteDagRun :execrows
-- Removes one run; its task_instances and XCom rows cascade (ON DELETE CASCADE).
DELETE FROM dag_runs WHERE dag_id = $1 AND run_id = $2;

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

-- name: ResetDagRunToVersion :exec
-- Clear re-binds the run to the DAG's current registered version (ADR 0020): a
-- re-run after a code/yaml fix picks up the newest image and config — in dev that
-- is the last hot-reload, in prod the last deploy — while everything within a
-- version stays reproducible.
UPDATE dag_runs
SET state = 'queued', started_at = NULL, ended_at = NULL, dag_version_id = $2
WHERE id = $1;

-- name: StampDagRunState :exec
-- Transitions a run's state and stamps the run's own timestamps so the UI can
-- show its duration: started_at on first entry into 'running', ended_at on a
-- terminal state. Other timestamps are preserved (the scheduler may re-run).
UPDATE dag_runs
SET state = sqlc.arg(state)::dag_run_state,
    started_at = CASE WHEN sqlc.arg(state)::dag_run_state = 'running' AND started_at IS NULL THEN now() ELSE started_at END,
    ended_at = CASE WHEN sqlc.arg(state)::dag_run_state IN ('success', 'failed') THEN now() ELSE ended_at END
WHERE id = sqlc.arg(id);

-- name: CreateTaskInstance :one
-- try_number starts at 1 to match Airflow (1-based attempts): the first run's
-- logs live at .../1.log, which is where the UI's log view looks. Retries bump
-- it via ResetForRetry.
INSERT INTO task_instances (tenant_id, dag_run_id, task_id, operator, max_tries, state, try_number)
VALUES ($1, $2, $3, $4, $5, $6, 1)
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
-- Returns each cron-scheduled DAG with the bits the scheduler needs to decide
-- both "is there a slot due?" (schedule + last_logical) and "how many slots
-- should I backfill on this tick?" (catchup + start_date, see #129).
SELECT d.dag_id, d.schedule, d.catchup, d.start_date,
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
-- Stamps the per-state entry timestamps the UI shows (scheduled_when /
-- queued_when / start_date). Each is set on first entry only ("IS NULL"), so a
-- re-emitted transition does not move the recorded time. $3 is cast to
-- task_state (see ReportTaskResult for why the cast is required).
UPDATE task_instances
SET state = sqlc.arg(state)::task_state,
    scheduled_at = CASE WHEN sqlc.arg(state)::task_state = 'scheduled' AND scheduled_at IS NULL THEN now() ELSE scheduled_at END,
    queued_at = CASE WHEN sqlc.arg(state)::task_state = 'queued' AND queued_at IS NULL THEN now() ELSE queued_at END,
    started_at = CASE WHEN sqlc.arg(state)::task_state = 'running' AND started_at IS NULL THEN now() ELSE started_at END
WHERE dag_run_id = sqlc.arg(dag_run_id) AND task_id = sqlc.arg(task_id);

-- name: ResetTaskInstanceToNone :exec
UPDATE task_instances
SET state = 'none', started_at = NULL, ended_at = NULL, try_number = try_number + 1
WHERE dag_run_id = $1 AND task_id = $2;

-- name: FailTaskInstanceIfActive :exec
UPDATE task_instances
SET state = 'failed', ended_at = now(), error_message = $2
WHERE id = $1 AND state IN ('scheduled', 'queued', 'running');

-- name: ReportTaskResult :exec
-- $3 is cast to task_state in every usage: without the cast Postgres deduces an
-- enum type from `state = $3` but text from the literal comparisons below and
-- rejects the parameter as having inconsistent types (SQLSTATE 42P08). The pod
-- agent path is the first to exercise this query end-to-end.
UPDATE task_instances
SET state = $3::task_state,
    exit_code = $4,
    error_message = $5,
    started_at = CASE WHEN $3::task_state = 'running' AND started_at IS NULL THEN now() ELSE started_at END,
    ended_at = CASE WHEN $3::task_state IN ('success', 'failed', 'skipped', 'upstream_failed') THEN now() ELSE ended_at END,
    duration_seconds = CASE WHEN $3::task_state IN ('success', 'failed') AND started_at IS NOT NULL
        THEN EXTRACT(EPOCH FROM (now() - started_at)) ELSE duration_seconds END
WHERE dag_run_id = $1 AND task_id = $2;

-- name: ResolveRunRef :one
SELECT t.id AS tenant_id, dr.id AS dag_run_id
FROM dag_runs dr
JOIN dags d ON d.id = dr.dag_id
JOIN tenants t ON t.id = d.tenant_id
WHERE t.name = $1 AND d.dag_id = $2 AND dr.run_id = $3;

-- name: LatestRunsForDags :many
SELECT d.dag_id AS dag_id_text,
       r.run_id, r.logical_date, r.state, r.trigger, r.queued_at, r.started_at, r.ended_at
FROM dags d
JOIN LATERAL (
    SELECT dr.run_id, dr.logical_date, dr.state, dr.trigger, dr.queued_at, dr.started_at, dr.ended_at
    FROM dag_runs dr
    WHERE dr.dag_id = d.id
    ORDER BY dr.logical_date DESC
    LIMIT $3
) r ON true
WHERE d.tenant_id = $1 AND d.dag_id = ANY($2::text[])
ORDER BY d.dag_id, r.logical_date DESC;

-- name: TaskInstancesForDagRuns :many
SELECT dr.run_id, ti.task_id, ti.try_number, ti.state,
       ti.started_at, ti.ended_at
FROM task_instances ti
JOIN dag_runs dr ON dr.id = ti.dag_run_id
JOIN dags d ON d.id = dr.dag_id
WHERE d.tenant_id = $1 AND d.dag_id = $2 AND dr.run_id = ANY($3::text[])
ORDER BY dr.run_id, ti.task_id, ti.try_number;

-- name: CountDagsByLatestRunState :many
SELECT lr.state AS state, count(*) AS n
FROM (
    SELECT DISTINCT ON (r.dag_id) r.state
    FROM dag_runs r
    JOIN dags d ON d.id = r.dag_id
    WHERE d.tenant_id = $1
    ORDER BY r.dag_id, r.logical_date DESC
) lr
GROUP BY lr.state;

-- name: CountDagRunStatesInWindow :many
SELECT r.state AS state, count(*) AS n
FROM dag_runs r
JOIN dags d ON d.id = r.dag_id
WHERE d.tenant_id = $1 AND r.logical_date >= $2 AND r.logical_date <= $3
GROUP BY r.state;

-- name: CountTaskInstanceStatesInWindow :many
SELECT ti.state AS state, count(*) AS n
FROM task_instances ti
JOIN dag_runs r ON r.id = ti.dag_run_id
JOIN dags d ON d.id = r.dag_id
WHERE d.tenant_id = $1 AND r.logical_date >= $2 AND r.logical_date <= $3
GROUP BY ti.state;

-- name: ResetFailedTaskInstance :execrows
UPDATE task_instances
SET state = 'none', started_at = NULL, ended_at = NULL, try_number = try_number + 1
WHERE dag_run_id = $1 AND task_id = $2
  AND state IN ('failed', 'upstream_failed', 'up_for_retry');

-- name: ResetAllFailedTaskInstances :execrows
UPDATE task_instances
SET state = 'none', started_at = NULL, ended_at = NULL, try_number = try_number + 1
WHERE dag_run_id = $1
  AND state IN ('failed', 'upstream_failed', 'up_for_retry');

-- name: SetTaskInstanceNote :exec
UPDATE task_instances
SET note = $3
WHERE dag_run_id = $1 AND task_id = $2;

-- name: ListOrphanCandidates :many
-- Lists dag_runs currently in 'running' whose task instances are ALL terminal
-- or never-started (no TI in scheduled/queued/running), alongside the
-- timestamp of their most recent observable activity. The "no active TI"
-- filter is the critical safety guarantee: a legitimately-active task (slow
-- image pull, long-running job) keeps its run out of the candidate set, so
-- the reaper can never kill a live execution. The shape this catches is the
-- post-crash one: TIs settled (success/failed/skipped/upstream_failed) but
-- FinalizeRun did not transition the dag_run — e.g. the server died between
-- the last TI report and the next scheduler tick. The LIMIT bounds a single
-- tick's reap work even after a multi-hour outage; the rest are picked up
-- on the next tick (the reaper is a backstop, not a sprint).
SELECT dr.id AS id,
       d.dag_id AS dag_id_text,
       GREATEST(
           COALESCE(MAX(ti.ended_at), 'epoch'::timestamptz),
           COALESCE(MAX(ti.started_at), 'epoch'::timestamptz),
           COALESCE(dr.started_at, 'epoch'::timestamptz),
           dr.queued_at
       )::timestamptz AS last_activity
FROM dag_runs dr
JOIN dags d ON d.id = dr.dag_id
LEFT JOIN task_instances ti ON ti.dag_run_id = dr.id
WHERE dr.state = 'running'
  AND NOT EXISTS (
      SELECT 1 FROM task_instances ti2
      WHERE ti2.dag_run_id = dr.id
        AND ti2.state IN ('scheduled', 'queued', 'running')
  )
GROUP BY dr.id, d.dag_id, dr.started_at, dr.queued_at
ORDER BY dr.queued_at
LIMIT 100;

-- name: MarkRunOrphanedTaskInstances :exec
-- Fails any still-active task instance under an orphaned run. Called together
-- with MarkRunOrphanedRun inside a single transaction (the repository owns the
-- atomicity); split because sqlc cannot generate a CTE+UPDATE that reuses one
-- parameter across an UPDATE-inside-WITH and the outer UPDATE.
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    error_message = 'orphaned: scheduler restart left this task without a runner'
WHERE dag_run_id = $1
  AND state IN ('scheduled', 'queued', 'running');

-- name: RecordTaskHeartbeat :exec
-- Stamps last_heartbeat_at on the active TI of an attempt. Bounded by the
-- (dag_run_id, task_id, try_number) tuple to match the agent's identity. The
-- state IN guard avoids stamping a heartbeat on a TI the scheduler already
-- transitioned to terminal between the agent's last heartbeat and now — a
-- terminal TI must stay terminal even if a late heartbeat arrives.
UPDATE task_instances
SET last_heartbeat_at = now()
WHERE dag_run_id = $1
  AND task_id = $2
  AND try_number = $3
  AND state IN ('queued', 'running');

-- name: ListAgentLostCandidates :many
-- Lists running TIs that have heartbeated at least once and whose latest
-- heartbeat is non-null, alongside enough identity to log + observe.
-- The "non-null" filter is the safety guarantee: a TI that never heartbeated
-- is either inline (no agent ever exists) or fresh — out of scope for this
-- reaper. The LIMIT bounds a single tick's reap work even after a large
-- outage; the rest are picked up on the next tick.
SELECT ti.id AS task_instance_id,
       ti.dag_run_id AS dag_run_id,
       d.dag_id AS dag_id_text,
       ti.task_id AS task_id,
       ti.last_heartbeat_at AS last_heartbeat_at
FROM task_instances ti
JOIN dag_runs dr ON dr.id = ti.dag_run_id
JOIN dags d ON d.id = dr.dag_id
WHERE ti.state = 'running'
  AND ti.last_heartbeat_at IS NOT NULL
ORDER BY ti.last_heartbeat_at
LIMIT 100;

-- name: MarkTaskDispatchFailed :exec
-- Fails a TI whose asynchronous dispatch (BufferedDispatcher worker) errored
-- inside the inner dispatcher. Targets the active row by (dag_run_id,
-- task_id) and the active states (scheduled/queued) — a TI that already
-- moved to running/terminal between the worker accepting the request and
-- the dispatch failing is left alone (defense in depth).
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    error_message = $3
WHERE dag_run_id = $1
  AND task_id = $2
  AND state IN ('scheduled', 'queued');

-- name: MarkTaskAgentLost :execrows
-- Fails a TI whose agent went silent. The WHERE state='running' guard
-- prevents overwriting a TI the agent's last terminal report finally
-- delivered between our list and our write (defense in depth — a late
-- report wins over the reaper). Idempotent on a second call.
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    error_message = 'agent_lost: no heartbeat within the threshold — see #128'
WHERE id = $1 AND state = 'running';

-- name: MarkRunOrphanedRun :execrows
-- Fails an orphaned dag run. The `state = 'running'` guard makes the reap a
-- safety net, never a takeover: a competing finalizer (the normal scheduler
-- path) cannot be overwritten. Idempotent: a second call on a run already
-- failed updates zero rows.
UPDATE dag_runs
SET state = 'failed',
    ended_at = now(),
    note = 'orphaned: no scheduler activity within the orphan window — see #120'
WHERE id = $1 AND state = 'running';
