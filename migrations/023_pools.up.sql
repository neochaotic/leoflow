-- Named task pools (Airflow's pools): a tenant-scoped, cross-DAG slot budget
-- that admission draws against so tasks from different DAGs share a bounded pool
-- of concurrency (ADR 0053 Stage 3). slots is the cap; a task in a pool is
-- admitted to `queued` only while the pool has a free slot. is_default marks the
-- implicit pool a task with no declared pool falls back to, so the gate is always
-- well-defined. Pools are a Pro-only concept; Lite never reads this table.
CREATE TABLE pools (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    slots INT NOT NULL DEFAULT 0,
    description TEXT,
    is_default BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pools_unique UNIQUE (tenant_id, name)
);

-- Seed the implicit default pool for the bootstrap tenant. 128 slots matches
-- Airflow's default_pool default, so an unconfigured deployment behaves the way
-- an Airflow operator expects: generous headroom, not an accidental throttle.
INSERT INTO pools (tenant_id, name, slots, description, is_default)
SELECT id, 'default_pool', 128, 'Default pool', true
FROM tenants WHERE name = 'default';
