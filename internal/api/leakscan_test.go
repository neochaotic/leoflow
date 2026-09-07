package api

import "strings"

// leakScanTarget returns body with every occurrence of an identifier the test
// itself chose replaced by a placeholder, so a scan for leaked database
// internals cannot be tripped by the handler echoing that identifier back.
//
// The identifiers these tests generate are nanosecond timestamps, and a
// 19-digit timestamp readily contains a 5-digit SQLSTATE by chance: the dag id
// api_dup_1788792423505739498 embeds 23505, which failed the scan in
// TestRegisterVersionDuplicateReturns409 on a response body that was entirely
// correct. Echoing back a caller-supplied path segment is not a leak, so it
// must not be scanned; everything the server composed itself still is.
func leakScanTarget(body string, echoed ...string) string {
	for _, e := range echoed {
		if e == "" {
			continue
		}
		body = strings.ReplaceAll(body, e, "<echoed>")
	}
	return body
}
