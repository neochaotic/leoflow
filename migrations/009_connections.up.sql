-- Airflow-style Connections: credentials/endpoints for operators, managed from
-- the Admin UI. password and extra are encrypted at rest (AES-256-GCM, ADR 0019);
-- non-secret metadata is stored in the clear so it stays queryable.
CREATE TABLE connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    conn_id TEXT NOT NULL,
    conn_type TEXT NOT NULL,
    host TEXT,
    conn_schema TEXT,
    login TEXT,
    password TEXT,            -- AES-256-GCM ciphertext (base64), never returned
    port INT,
    extra TEXT,               -- AES-256-GCM ciphertext (base64)
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT connections_unique UNIQUE (tenant_id, conn_id)
);
