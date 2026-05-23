-- Airflow-style Variables: tenant-scoped key/value config consumed by DAGs and
-- managed from the Admin UI. Stored as plaintext for the MVP; secret-ish keys
-- are masked in API responses. Encryption-at-rest is a follow-up (see #45 / the
-- connections ADR).
CREATE TABLE variables (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    value TEXT NOT NULL DEFAULT '',
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT variables_unique UNIQUE (tenant_id, key)
);
