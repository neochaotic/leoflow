package scheduler

import (
	"fmt"
	"testing"
	"time"
)

// TestInfraReplaceMaxDelayBoundsEveryReplace: the exported ceiling is a true
// upper bound on the delay readyToInfraReplace can impose on any infra-failed
// task still within its re-place budget — the backoff before the last permitted
// re-place plus the whole jitter window. The orphan-run reaper's idle threshold
// (owned by the executor package) must sit above this value, or a run whose only
// live task is parked in its longest re-place backoff is reaped as orphaned
// while it is still recovering; the boot-time resilience ladder holds the two
// values apart, so this test pins what the ladder is told.
func TestInfraReplaceMaxDelayBoundsEveryReplace(t *testing.T) {
	ceiling := InfraReplaceMaxDelay()
	if want := dispatchBackoff(infraMaxAttempts) + infraReplaceJitterWindow; ceiling != want {
		t.Fatalf("InfraReplaceMaxDelay() = %v, want backoff(infraMaxAttempts)+jitter window = %v", ceiling, want)
	}
	if ceiling != 190*time.Second {
		t.Errorf("InfraReplaceMaxDelay() = %v; the shipped budget is 190s — if this moved on purpose, re-check it against the orphan threshold", ceiling)
	}
	// Every re-placeable attempt count (0..infraMaxAttempts-1, so backoff index
	// 1..infraMaxAttempts) with every sampled jitter stays strictly under it.
	for attempts := 0; attempts < infraMaxAttempts; attempts++ {
		for i := 0; i < 64; i++ {
			runID, taskID := fmt.Sprintf("run-%d", i), fmt.Sprintf("task-%d", i*7)
			delay := dispatchBackoff(attempts+1) + infraReplaceJitter(runID, taskID)
			if delay >= ceiling {
				t.Fatalf("infra_attempts=%d %s/%s: re-place delay %v is not below the ceiling %v", attempts, runID, taskID, delay, ceiling)
			}
		}
	}
}
