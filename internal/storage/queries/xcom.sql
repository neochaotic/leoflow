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

-- name: GetXComByNames :one
SELECT x.redis_key, x.content_type, x.size_bytes, x.created_at
FROM xcom_index x
JOIN dag_runs dr ON dr.id = x.dag_run_id
JOIN dags d ON d.id = dr.dag_id
JOIN tenants t ON t.id = d.tenant_id
WHERE t.name = $1 AND d.dag_id = $2 AND dr.run_id = $3 AND x.task_id = $4 AND x.key = $5
  AND x.expires_at > now();
