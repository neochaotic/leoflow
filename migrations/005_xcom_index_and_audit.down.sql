-- 005_xcom_index_and_audit.down.sql
-- Reverse the XCom index and audit log.

BEGIN;

DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS xcom_index;

COMMIT;
