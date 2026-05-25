-- name: RecordStagingVolume :exec
INSERT INTO staging_volumes (tenant_id, dag_id, run_id, pvc_name, size, state)
VALUES (sqlc.arg(tenant_id), sqlc.arg(dag_id), sqlc.arg(run_id), sqlc.arg(pvc_name), sqlc.arg(size), 'active')
ON CONFLICT (pvc_name) DO NOTHING;

-- name: MarkStagingDeleted :exec
UPDATE staging_volumes
SET state = 'deleted', deleted_at = now(), delete_reason = sqlc.arg(reason)
WHERE pvc_name = sqlc.arg(pvc_name) AND state <> 'deleted';

-- name: ListActiveStagingVolumes :many
SELECT s.pvc_name, r.state AS run_state, r.ended_at AS run_ended_at
FROM staging_volumes s
LEFT JOIN dags d ON d.tenant_id = s.tenant_id AND d.dag_id = s.dag_id
LEFT JOIN dag_runs r ON r.dag_id = d.id AND r.run_id = s.run_id
WHERE s.state = 'active';
