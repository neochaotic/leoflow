package executor

import (
	"strings"
	"testing"
	"time"
)

// defaultLadder is the production ladder as the server wires it: the agent's
// heartbeat interval and token TTL, the default reaper thresholds/graces, and
// the reconciler's sweep interval.
func defaultLadder() ResilienceLadder {
	cfg := DefaultReaperConfig()
	return ResilienceLadder{
		HeartbeatInterval:  15 * time.Second,
		AgentLostThreshold: cfg.AgentLostThreshold,
		AgentLostGrace:     cfg.AgentLostGrace,
		PodLostLeaderGrace: cfg.PodLostLeaderGrace,
		AttemptTokenTTL:    10 * time.Minute,
		ReconcileInterval:  30 * time.Second,
	}
}

// TestValidateResilienceLadderDefaultsPass: the shipped defaults satisfy every
// ordering the control-plane-restart recovery depends on.
func TestValidateResilienceLadderDefaultsPass(t *testing.T) {
	if err := ValidateResilienceLadder(defaultLadder()); err != nil {
		t.Fatalf("default ladder must validate, got %v", err)
	}
}

// TestValidateResilienceLadderRejectsEachViolation: each single inverted (or
// equal — the relations are strict) rung fails, and the error names BOTH sides
// of the violated relation so an operator can tell which knob to move.
func TestValidateResilienceLadderRejectsEachViolation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ResilienceLadder)
		want   []string // substrings the error must carry
	}{
		{
			name:   "heartbeat interval not below agent-lost threshold",
			mutate: func(l *ResilienceLadder) { l.HeartbeatInterval = l.AgentLostThreshold },
			want:   []string{"heartbeat interval", "agent-lost threshold"},
		},
		{
			name:   "agent-lost threshold not below agent-lost grace",
			mutate: func(l *ResilienceLadder) { l.AgentLostGrace = l.AgentLostThreshold },
			want:   []string{"agent-lost threshold", "agent-lost grace"},
		},
		{
			name:   "agent-lost grace not below attempt token TTL",
			mutate: func(l *ResilienceLadder) { l.AttemptTokenTTL = l.AgentLostGrace },
			want:   []string{"agent-lost grace", "attempt token TTL"},
		},
		{
			name:   "reconcile interval not below agent-lost grace",
			mutate: func(l *ResilienceLadder) { l.ReconcileInterval = l.AgentLostGrace },
			want:   []string{"reconcile interval", "agent-lost grace"},
		},
		{
			name:   "reconcile interval not below pod-lost leader grace",
			mutate: func(l *ResilienceLadder) { l.PodLostLeaderGrace = l.ReconcileInterval },
			want:   []string{"reconcile interval", "pod-lost leader grace"},
		},
		{
			name:   "a non-positive rung",
			mutate: func(l *ResilienceLadder) { l.HeartbeatInterval = 0 },
			want:   []string{"heartbeat interval", "positive"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := defaultLadder()
			tc.mutate(&l)
			err := ValidateResilienceLadder(l)
			if err == nil {
				t.Fatalf("ladder %+v must be rejected", l)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q must name %q", err, w)
				}
			}
		})
	}
}
