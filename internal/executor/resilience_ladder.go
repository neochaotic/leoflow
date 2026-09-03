package executor

import (
	"fmt"
	"time"
)

// ResilienceLadder is the set of timing knobs whose relative ORDER the
// control-plane-restart recovery depends on. Each is owned by a different
// package (agent, executor, scheduler, server main, operator config) and tuned
// for its own reason, so nothing but this type states that they must line up.
// The invariants:
//
//	heartbeat interval  <  agent-lost threshold  <  agent-lost grace  <  attempt token TTL
//	2 × reconcile interval  <  agent-lost grace
//	2 × reconcile interval  <  pod-lost leader grace
//	longest infra re-place delay  <  orphan threshold
//	attempt token TTL  <  max attempt credential lifetime   (when the ceiling is enabled)
//
// Why each rung matters:
//   - heartbeat < threshold: a healthy agent must beat well inside the silence
//     window or the agent-lost reaper fails live tasks.
//   - threshold < agent-lost grace: after a (re-)election the reaper waits
//     longer than the silence it punishes, so the whole fleet can re-heartbeat.
//   - agent-lost grace < token TTL: the grace ends while a re-heartbeat can still
//     authenticate and renew the bearer; otherwise the fleet is unreapable AND
//     unable to report — every in-flight task is lost.
//   - 2×reconcile < agent-lost grace, 2×reconcile < pod-lost leader grace: the
//     reconciler gets at least two full sweeps under the new leader before any
//     reaper may fail a task whose pod finished during the outage, so the
//     durable outcome record is recovered as the truth before a reaper guesses.
//     A single sweep is not enough: the first tick under a new leader may land
//     anywhere in the interval, so "one interval below the grace" can mean zero
//     completed sweeps.
//   - infra re-place delay < orphan threshold: a run whose only live task is
//     parked in its longest infra re-place backoff has no activity to show the
//     orphan-run reaper; the threshold must outlast that parking or the reaper
//     eats a run that is still recovering from the very fault being retried.
//   - token TTL < credential lifetime: heartbeat renewal keeps an attempt's
//     bearer alive only while the attempt is younger than the ceiling. A ceiling
//     below the TTL means the first renewal is already refused, the bearer
//     lapses at the TTL, and every task longer than one TTL is unreapable AND
//     unable to report — the restart recovery is silently disabled. This is the
//     ONLY operator-tunable rung; every other value is a build-time constant.
//
// Warm pools share the same ladder: a warm worker's per-attempt bearer is renewed
// by the same heartbeat under the same ceiling, and the warm-worker-lost reaper
// reuses the pod-lost mark, so no warm-specific rung exists.
type ResilienceLadder struct {
	HeartbeatInterval  time.Duration
	AgentLostThreshold time.Duration
	AgentLostGrace     time.Duration
	PodLostLeaderGrace time.Duration
	AttemptTokenTTL    time.Duration
	ReconcileInterval  time.Duration
	// OrphanThreshold is how long a running dag run may sit with no activity
	// before the orphan-run reaper fails it (ReaperConfig.OrphanThreshold).
	OrphanThreshold time.Duration
	// InfraReplaceMaxDelay is the longest the scheduler may park an infra-failed
	// task before re-placing it: the backoff before the final permitted re-place
	// plus the whole de-synchronizing jitter window. The scheduler owns and
	// computes it; it is passed in because the scheduler package depends on this
	// one, so the validator cannot read it directly.
	InfraReplaceMaxDelay time.Duration
	// MaxAttemptCredentialLifetime is the operator's auth.max_attempt_credential_lifetime:
	// the ceiling on how long heartbeat renewal keeps an attempt's bearer alive.
	// A non-positive value is the documented "no ceiling" setting; the rung that
	// depends on it is then trivially satisfied and skipped.
	MaxAttemptCredentialLifetime time.Duration
}

// maxAttemptCredentialLifetimeKey is the config key of the one operator-tunable
// rung, so the boot error points the operator at the knob that has to move.
const maxAttemptCredentialLifetimeKey = "auth.max_attempt_credential_lifetime"

// ladderRung names one knob (or a fixed multiple of one) for error messages.
type ladderRung struct {
	name string
	d    time.Duration
}

// ladderRelation is one strict ordering lo < hi. operator marks the relation's
// hi side as an operator-tunable config value: the error then names the config
// key; otherwise every value is a build-time constant and the only remedy is a
// code change, so the error asks for a bug report.
type ladderRelation struct {
	lo, hi   ladderRung
	why      string
	operator bool
}

// ValidateResilienceLadder checks the orderings the restart recovery depends on
// (see ResilienceLadder) and reports the first violated relation, naming both
// sides with their values. A relation between build-time constants can only be
// broken by a code change, so its error says so and asks for a bug report; the
// one relation involving an operator knob names the config key. All build-time
// rungs must be positive. It is pure — the server calls it once at boot and
// refuses to start on an error, turning what used to be a comment-level
// convention into an enforced invariant.
func ValidateResilienceLadder(l ResilienceLadder) error {
	hb := ladderRung{"heartbeat interval", l.HeartbeatInterval}
	thr := ladderRung{"agent-lost threshold", l.AgentLostThreshold}
	agrace := ladderRung{"agent-lost grace", l.AgentLostGrace}
	pgrace := ladderRung{"pod-lost leader grace", l.PodLostLeaderGrace}
	ttl := ladderRung{"attempt token TTL", l.AttemptTokenTTL}
	rec := ladderRung{"reconcile interval", l.ReconcileInterval}
	orphan := ladderRung{"orphan threshold", l.OrphanThreshold}
	replace := ladderRung{"longest infra re-place delay", l.InfraReplaceMaxDelay}

	for _, r := range []ladderRung{hb, thr, agrace, pgrace, ttl, rec, orphan, replace} {
		if r.d <= 0 {
			return fmt.Errorf("resilience ladder: %s (%v) must be positive", r.name, r.d)
		}
	}
	twoRec := ladderRung{"2 × reconcile interval", 2 * l.ReconcileInterval}
	relations := []ladderRelation{
		{lo: hb, hi: thr, why: "a live agent must heartbeat well inside the agent-lost silence window"},
		{lo: thr, hi: agrace, why: "the post-leadership grace must outlast the silence it forgives so the fleet can re-heartbeat"},
		{lo: agrace, hi: ttl, why: "a re-heartbeat must still authenticate and renew the bearer when the grace ends"},
		{lo: twoRec, hi: agrace, why: "the reconciler must complete at least two sweeps under a new leader before agent-lost may reap"},
		{lo: twoRec, hi: pgrace, why: "the reconciler must complete at least two sweeps to recover durable pod outcomes before pod-lost may reap"},
		{lo: replace, hi: orphan, why: "a run parked in its longest infra re-place backoff must not be reaped as orphaned while it is still recovering"},
	}
	if l.MaxAttemptCredentialLifetime > 0 {
		ceiling := ladderRung{maxAttemptCredentialLifetimeKey, l.MaxAttemptCredentialLifetime}
		relations = append(relations, ladderRelation{
			lo: ttl, hi: ceiling, operator: true,
			why: "heartbeat renewal must be able to extend the bearer at least once, or every task longer than one TTL loses its credential and the restart recovery is disabled",
		})
	}
	for _, rel := range relations {
		if rel.lo.d < rel.hi.d {
			continue
		}
		if rel.operator {
			return fmt.Errorf("resilience ladder: %s (%v) must be < %s (%v): %s — raise %s",
				rel.lo.name, rel.lo.d, rel.hi.name, rel.hi.d, rel.why, rel.hi.name)
		}
		return fmt.Errorf("resilience ladder: %s (%v) must be < %s (%v): %s — build-time invariant violated, please file a bug",
			rel.lo.name, rel.lo.d, rel.hi.name, rel.hi.d, rel.why)
	}
	return nil
}
