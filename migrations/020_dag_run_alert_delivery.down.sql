-- Reverse 020: drop the delivery-retry bookkeeping, returning to 019's single
-- claim-before-send marker. Runs mid-retry lose their attempt count and are
-- treated as never attempted by the older code, which claims and sends once.
BEGIN;

ALTER TABLE dag_runs DROP COLUMN IF EXISTS next_alert_attempt_at;
ALTER TABLE dag_runs DROP COLUMN IF EXISTS alert_attempts;

COMMIT;
