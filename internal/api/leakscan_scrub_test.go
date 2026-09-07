package api

import (
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

	scrubbed := leakScanTarget(body, dagID)
	for _, leak := range pgLeaks {
		if !strings.Contains(scrubbed, leak) {
			t.Errorf("scrubbing hid a real leak (%q) from the scan: %s", leak, scrubbed)
		}
	}
}
