package agent

import "testing"

// TestOOMCounterParsing pins the two cgroup formats and, more importantly, the
// UNKNOWN case.
//
// Unreadable must not read as zero. An unreadable "before" of 0 against a
// readable "after" of 1 looks exactly like a kill that may never have happened,
// and the consequence is a failure message telling someone to raise a memory
// limit that was never the problem. That is worse than the generic message it
// replaces, so the counter carries whether it knows at all.
func TestOOMCounterParsing(t *testing.T) {
	// cgroup v2 memory.events, as the kernel writes it.
	const v2 = "low 0\nhigh 0\nmax 12\noom 3\noom_kill 2\noom_group_kill 0\n"
	if got, ok := parseOOMFieldFrom(v2, "oom_kill"); !ok || got != 2 {
		t.Fatalf("cgroup v2 oom_kill = %d, ok=%v; want 2, true", got, ok)
	}
	// `oom` and `oom_kill` are different fields and the wrong one is plausible:
	// `oom` counts times the cgroup went out of memory, not kills.
	if got, _ := parseOOMFieldFrom(v2, "oom"); got != 3 {
		t.Fatalf("the parser conflated oom with oom_kill: got %d for oom, want 3", got)
	}

	const v1 = "oom_kill_disable 0\nunder_oom 0\noom_kill 5\n"
	if got, ok := parseOOMFieldFrom(v1, "oom_kill"); !ok || got != 5 {
		t.Fatalf("cgroup v1 oom_kill = %d, ok=%v; want 5, true", got, ok)
	}

	for _, tc := range []struct{ name, content string }{
		{"an absent field", "low 0\nhigh 0\n"},
		{"an empty file", ""},
		{"a value that is not a number", "oom_kill banana\n"},
	} {
		if _, ok := parseOOMFieldFrom(tc.content, "oom_kill"); ok {
			t.Errorf("%s reported a usable counter; it must report unknown", tc.name)
		}
	}
}

// TestOOMKilledBetween pins that a verdict needs BOTH samples.
func TestOOMKilledBetween(t *testing.T) {
	known := func(n int64) oomCounter { return oomCounter{kills: n, known: true} }
	unknown := oomCounter{known: false}

	if !oomKilledBetween(known(0), known(1)) {
		t.Error("a counter that rose is an OOM kill and must be reported")
	}
	if !oomKilledBetween(known(4), known(9)) {
		t.Error("several kills in one run is still a kill")
	}
	if oomKilledBetween(known(2), known(2)) {
		t.Error("an unchanged counter is not a kill; this is the common failing task")
	}
	// The cgroup is shared with whatever ran before, so a nonzero baseline is
	// normal on a warm worker and must not read as this attempt's kill.
	if oomKilledBetween(known(7), known(7)) {
		t.Error("a nonzero counter that did not move is a previous attempt's kill, not this one's")
	}
	// Both directions of not-knowing, because guessing here mislabels a failure.
	if oomKilledBetween(unknown, known(1)) {
		t.Error("an unknown baseline must not let any later reading look like a rise")
	}
	if oomKilledBetween(known(0), unknown) {
		t.Error("an unreadable final sample is not evidence of a kill")
	}
	if oomKilledBetween(unknown, unknown) {
		t.Error("knowing nothing is not evidence")
	}
	// A counter that went DOWN is a cgroup that was replaced under us, not a
	// kill run in reverse.
	if oomKilledBetween(known(5), known(1)) {
		t.Error("a falling counter is a different cgroup, not an OOM")
	}
}
