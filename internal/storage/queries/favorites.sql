-- name: AddFavorite :exec
INSERT INTO dag_favorites (tenant_id, user_id, dag_id)
SELECT t.id, sqlc.arg(user_id), sqlc.arg(dag_id)
FROM tenants t
WHERE t.name = sqlc.arg(tenant)
ON CONFLICT (tenant_id, user_id, dag_id) DO NOTHING;

-- name: RemoveFavorite :exec
DELETE FROM dag_favorites
WHERE tenant_id = (SELECT id FROM tenants WHERE name = sqlc.arg(tenant))
  AND user_id = sqlc.arg(user_id)
  AND dag_id = sqlc.arg(dag_id);

-- name: ListFavoriteDagIDs :many
SELECT f.dag_id
FROM dag_favorites f
JOIN tenants t ON t.id = f.tenant_id
WHERE t.name = sqlc.arg(tenant) AND f.user_id = sqlc.arg(user_id);
