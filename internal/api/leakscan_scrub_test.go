package api

import (
	"slices"
	"strings"
	"testing"
)

// pgLeaks are the database internals no error body may carry to a client.
var pgLeaks = []string{"23505", "dag_versions_unique", "SQLSTATE"}

func TestLeakScanTargetIgnoresTheEchoedIdentifier(t *testing.T) {
	// The dag id and body from the observed failure, where the timestamp
	// itself contained 23505 and the response was correct.
	const dagID = "api_dup_1788792423505739498"
	body := `{"type":"about:blank","title":"conflict","status":409,` +
		`"detail":"inserting version: resource already exists",` +
		`"instance":"/api/v2/dags/` + dagID + `/versions"}`

	for _, leak := range pgLeaks {
		if strings.Contains(leakScanTarget(body, dagID), leak) {
			t.Errorf("scrubbed body still trips on %q, so a correct 409 fails the scan: %s",
				leak, leakScanTarget(body, dagID))
		}
	}
}

func TestLeakScanTargetStillSeesARealLeak(t *testing.T) {
	// Scrubbing must not blind the scan: the same id, but the server composed
	// the constraint name and SQLSTATE into the detail.
	const dagID = "api_dup_1788792423505739498"
	body := `{"title":"internal","status":500,` +
		`"detail":"inserting version: ERROR: duplicate key value violates ` +
		`unique constraint \"dag_versions_unique\" (SQLSTATE 23505)",` +
		`"instance":"/api/v2/dags/` + dagID + `/versions"}`

	// Assert against the tokens THIS fixture carries, not pgLeaks: iterating
	// the shared list would require every future addition to it to appear in
	// this hand-written body, so hardening the scan would turn the fast suite
	// red and the obvious "fix" would be to un-harden it.
	scrubbed := leakScanTarget(body, dagID)
	for _, leak := range []string{"23505", "dag_versions_unique", "SQLSTATE"} {
		if !strings.Contains(scrubbed, leak) {
			t.Errorf("scrubbing hid a real leak (%q) from the scan: %s", leak, scrubbed)
		}
	}
}

// TestPgLeaksCoversTheKnownPgInternals guards the other direction. Deleting a
// token from pgLeaks makes every test above easier and silently weakens the
// integration assertion that consumes it, so the floor is pinned here.
func TestPgLeaksCoversTheKnownPgInternals(t *testing.T) {
	for _, want := range []string{"23505", "dag_versions_unique", "SQLSTATE"} {
		if !slices.Contains(pgLeaks, want) {
			t.Errorf("pgLeaks no longer covers %q; the 409 leak scan is weaker than it was", want)
		}
	}
}
