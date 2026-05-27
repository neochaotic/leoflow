-- Postgres-backed XCom value store for Leoflow Lite, where the embedded
-- single-node runtime drops Redis entirely (locks already use pg advisory
-- locks). Production keeps the Redis backend per ADR 0006; this table is used
-- only by the Lite Postgres XCom backend. Values are TTL'd via expires_at:
-- reads filter on it and a periodic sweep deletes expired rows.
CREATE TABLE xcom_store (
    xcom_key     TEXT PRIMARY KEY,
    value        BYTEA NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/json',
    size_bytes   INTEGER NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_xcom_store_expires_at ON xcom_store (expires_at);
