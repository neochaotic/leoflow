package scheduler

import (
	"testing"
	"time"
)

// dispatchBackoff must grow exponentially from a small base and cap, so a
// transient control-plane blip is ridden out quickly while a permanent
// misconfiguration is not retried more often than the cap. It is deterministic
// (no jitter) so the planner's gate is testable.
func TestDispatchBackoff(t *testing.T) {
	for _, tc := range []struct {
		attempts int
		want     time.Duration
	}{
		{0, dispatchBackoffBase}, // defensive: pre-first-failure treated as base
		{1, 5 * time.Second},     // first failure
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{4, 40 * time.Second},
		{5, 80 * time.Second},
		{6, 160 * time.Second},
		{7, dispatchBackoffCap},  // 320s would exceed the cap
		{50, dispatchBackoffCap}, // far past the cap stays capped
	} {
		if got := dispatchBackoff(tc.attempts); got != tc.want {
			t.Errorf("dispatchBackoff(%d) = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}

// The cap must actually bound the largest value the sequence can produce.
func TestDispatchBackoffNeverExceedsCap(t *testing.T) {
	for a := 0; a <= 100; a++ {
		if d := dispatchBackoff(a); d > dispatchBackoffCap {
			t.Fatalf("dispatchBackoff(%d) = %s exceeds cap %s", a, d, dispatchBackoffCap)
		}
	}
}
