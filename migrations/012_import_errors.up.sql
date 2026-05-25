-- DAG parse/compile failures, surfaced as Airflow's "Import Errors" banner on the
-- home/dashboard. The `leoflow dev` watcher pushes an entry when a compile fails
-- and clears it on the next good compile; GET /api/v2/importErrors reads the feed.
-- One row per (tenant, filename) — a re-import replaces the previous error.
CREATE TABLE import_errors (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    stacktrace TEXT NOT NULL,
    bundle_name TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT import_errors_unique UNIQUE (tenant_id, filename)
);

CREATE INDEX idx_import_errors_tenant ON import_errors(tenant_id);
