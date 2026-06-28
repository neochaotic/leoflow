package cli

import "testing"

// TestMakeDeleteDagRemintsToken: the deregister callback mints a FRESH token on
// every call. A long-running Lite would otherwise reuse the one token it minted at
// startup, which expires after an hour — silently breaking hot-reload registration
// (#407). The HTTP call fails here (no server), but the mint must still happen per
// call.
func TestMakeDeleteDagRemintsToken(t *testing.T) {
	var calls int
	del := makeDeleteDag(func() string { calls++; return "" }, "http://127.0.0.1:0", t.TempDir(), func(string, ...any) {})
	_ = del("a")
	_ = del("b")
	if calls != 2 {
		t.Errorf("token minted %d times across 2 deregisters, want 2 (fresh per call, #407)", calls)
	}
}
