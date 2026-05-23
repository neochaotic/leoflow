-- Per-user DAG favorites, toggled by the star in the DAG list. Scoped to the
-- tenant and the user; the list endpoint sets is_favorite from this table.
CREATE TABLE dag_favorites (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL,
    dag_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT dag_favorites_unique UNIQUE (tenant_id, user_id, dag_id)
);

CREATE INDEX idx_dag_favorites_user ON dag_favorites(tenant_id, user_id);
