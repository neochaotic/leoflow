-- 020_dag_run_alert_delivery.up.sql
-- Stop losing on-failure pages (#463 sibling; found by hands-on validation of
-- v0.1.2-rc.1).
--
-- 019 gave dag_runs a single `alerted_at`, claimed BEFORE the send. That conflated
-- two different questions — "have we already tried this episode?" (dedup) and "did
-- the page get through?" (delivery) — so any send that failed after the claim was
-- lost for good. Measured on rc.1: a receiver answering 500 left the run marked
-- alerted with no retry, and 15 simultaneous failures produced 7 semaphore drops
-- plus 8 send failures with all 15 rows marked alerted — zero delivered, nothing
-- retryable. That is a correlated failure: the alert path is least reliable exactly
-- during the mass-failure incident it exists to report.
--
-- Split the two meanings. `alerted_at` now means DELIVERED and is stamped only
-- after a successful send, so an undelivered episode stays claimable. The attempt
-- itself is tracked separately, with a next-attempt time, so a retry cannot become
-- a hammer: a run stays failed forever, and without backoff a dead endpoint would
-- be retried once per scheduler tick (1s by default) for as long as the run exists.

BEGIN;

ALTER TABLE dag_runs ADD COLUMN IF NOT EXISTS alert_attempts integer NOT NULL DEFAULT 0;
ALTER TABLE dag_runs ADD COLUMN IF NOT EXISTS next_alert_attempt_at timestamptz;

-- Runs that already alerted under the 019 semantics keep counting as delivered:
-- alerted_at is non-NULL for them, and the new claim predicate requires it to be
-- NULL, so no historical run re-pages on upgrade.

COMMIT;
