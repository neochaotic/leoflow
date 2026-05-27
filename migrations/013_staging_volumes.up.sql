-- Per-DAG-run staging volume lifecycle, tracked in the metadatabase so GC is
-- deterministic and the "disk deleted" status is auditable (ADR 0022). One row
-- per PVC: provisioning upserts 'active'; GC marks 'deleted' with the reason
-- (run_succeeded | ttl_expired | orphaned). A successful run frees the volume
-- immediately; a failed run keeps it for the post-terminal TTL (re-run safety).
CREATE TABLE staging_volumes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    dag_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    pvc_name TEXT NOT NULL UNIQUE,
    size TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    delete_reason TEXT
);

CREATE INDEX idx_staging_volumes_active ON staging_volumes(tenant_id, dag_id, run_id) WHERE state = 'active';
