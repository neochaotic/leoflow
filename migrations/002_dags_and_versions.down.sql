-- 002_dags_and_versions.down.sql
-- Reverse DAGs and versioned snapshots.

BEGIN;

-- Drop the circular FK first so the tables can be removed.
ALTER TABLE dags DROP CONSTRAINT IF EXISTS dags_current_version_fk;

DROP TABLE IF EXISTS dag_versions;
DROP TABLE IF EXISTS dags;

COMMIT;
