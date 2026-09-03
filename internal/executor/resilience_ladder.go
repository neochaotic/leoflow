package executor

import (
	"fmt"
	"time"
)

// ResilienceLadder is the set of timing knobs whose relative ORDER the
// control-plane-restart recovery depends on. Each is owned by a different
// package (agent, executor, server main) and tuned for its own reason, so
// nothing but this type states that they must line up. The invariants:
//
//	heartbeat interval  <  agent-lost threshold  <  agent-lost grace  <  attempt token TTL
//	reconcile interval  <  agent-lost grace
//	reconcile interval  <  pod-lost leader grace
//
// Why each rung matters:
//   - heartbeat < threshold: a healthy agent must beat well inside the silence
//     window or the agent-lost reaper fails live tasks.
//   - threshold < agent-lost grace: after a (re-)election the reaper waits
//     longer than the silence it punishes, so the whole fleet can re-heartbeat.
//   - agent-lost grace < token TTL: the grace ends while a re-heartbeat can still
//     authenticate and renew the bearer; otherwise the fleet is unreapable AND
//     unable to report — every in-flight task is lost.
//   - reconcile < agent-lost grace, reconcile < pod-lost leader grace: the
//     reconciler gets at least one full sweep under the new leader before any
//     reaper may fail a task whose pod finished during the outage, so the
//     durable outcome record is recovered as the truth before a reaper guesses.
type ResilienceLadder struct {
	HeartbeatInterval  time.Duration
	AgentLostThreshold time.Duration
	AgentLostGrace     time.Duration
	PodLostLeaderGrace time.Duration
	AttemptTokenTTL    time.Duration
	ReconcileInterval  time.Duration
}

// ladderRung names one knob for error messages.
type ladderRung struct {
	name string
	d    time.Duration
}

// ValidateResilienceLadder checks the orderings the restart recovery depends on
// (see ResilienceLadder) and reports the first violated relation, naming both
// sides with their values so the operator knows which knob moved. All rungs
// must be positive. It is pure — the server calls it once at boot and refuses
// to start on an error, turning what used to be a comment-level convention into
// an enforced invariant.
func ValidateResilienceLadder(l ResilienceLadder) error {
	hb := ladderRung{"heartbeat interval", l.HeartbeatInterval}
	thr := ladderRung{"agent-lost threshold", l.AgentLostThreshold}
	agrace := ladderRung{"agent-lost grace", l.AgentLostGrace}
	pgrace := ladderRung{"pod-lost leader grace", l.PodLostLeaderGrace}
	ttl := ladderRung{"attempt token TTL", l.AttemptTokenTTL}
	rec := ladderRung{"reconcile interval", l.ReconcileInterval}

	for _, r := range []ladderRung{hb, thr, agrace, pgrace, ttl, rec} {
		if r.d <= 0 {
			return fmt.Errorf("resilience ladder: %s (%v) must be positive", r.name, r.d)
		}
	}
	relations := []struct {
		lo, hi ladderRung
		why    string
	}{
		{hb, thr, "a live agent must heartbeat well inside the agent-lost silence window"},
		{thr, agrace, "the post-leadership grace must outlast the silence it forgives so the fleet can re-heartbeat"},
		{agrace, ttl, "a re-heartbeat must still authenticate and renew the bearer when the grace ends"},
		{rec, agrace, "the reconciler must sweep at least once under a new leader before agent-lost may reap"},
		{rec, pgrace, "the reconciler must recover durable pod outcomes before pod-lost may reap"},
	}
	for _, rel := range relations {
		if rel.lo.d >= rel.hi.d {
			return fmt.Errorf("resilience ladder: %s (%v) must be < %s (%v): %s",
				rel.lo.name, rel.lo.d, rel.hi.name, rel.hi.d, rel.why)
		}
	}
	return nil
}
