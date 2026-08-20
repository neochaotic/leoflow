package config

import "testing"

// TestEffectiveMinIdle locks the model-A2 effective-warm-target formula (ADR 0058
// N1b2b): the per-DAG author-declared min_idle is clamped to [0, max_pool_size]
// with a fall back to the operator's execution.min_idle_workers when the DAG sets
// none, and the whole thing is gated by warm_pools_enabled (off => always 0).
func TestEffectiveMinIdle(t *testing.T) {
	cases := []struct {
		name       string
		enabled    bool
		minIdle    int // execution.min_idle_workers (operator fallback)
		maxPool    int // execution.max_pool_size (operator cap)
		dagMinIdle int // the DAG author's declared min_idle_workers
		want       int
	}{
		// Gate: warm pools off => never warm, whatever the DAG or operator asked.
		{"off ignores dag", false, 5, 8, 4, 0},
		{"off ignores operator floor", false, 3, 8, 0, 0},
		// DAG author declares a target within the cap => used verbatim.
		{"dag within cap", true, 0, 8, 2, 2},
		{"dag equal cap", true, 0, 8, 8, 8},
		// Author over-asks => clamped to the operator's max_pool_size.
		{"dag over cap clamped", true, 0, 8, 20, 8},
		// Author declares none (0) => fall back to the operator floor, then clamp.
		{"fallback to operator floor", true, 3, 8, 0, 3},
		{"fallback clamped by cap", true, 20, 8, 0, 8},
		{"fallback zero stays zero", true, 0, 8, 0, 0},
		// A negative (nonsensical) author value floors at 0, never a fallback.
		{"negative dag floors at zero", true, 5, 8, -1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := ExecutionSection{
				WarmPoolsEnabled: c.enabled,
				MinIdleWorkers:   c.minIdle,
				MaxPoolSize:      c.maxPool,
			}
			if got := e.EffectiveMinIdle(c.dagMinIdle); got != c.want {
				t.Errorf("EffectiveMinIdle(%d) = %d, want %d (enabled=%v floor=%d cap=%d)",
					c.dagMinIdle, got, c.want, c.enabled, c.minIdle, c.maxPool)
			}
		})
	}
}
