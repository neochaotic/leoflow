-- name: RecordXCom :exec
INSERT INTO xcom_index (tenant_id, dag_run_id, task_id, key, redis_key, size_bytes, content_type, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (dag_run_id, task_id, key) DO UPDATE SET
    redis_key = EXCLUDED.redis_key,
    size_bytes = EXCLUDED.size_bytes,
    content_type = EXCLUDED.content_type,
    created_at = now(),
    expires_at = EXCLUDED.expires_at;

-- name: GetXComEntry :one
SELECT redis_key, content_type, size_bytes, created_at
FROM xcom_index
WHERE dag_run_id = $1 AND task_id = $2 AND key = $3 AND expires_at > now();

-- name: DeleteExpiredXComIndex :exec
DELETE FROM xcom_index WHERE expires_at <= now();
