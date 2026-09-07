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
// correct. Echoing back a caller-supplied path segment is not a leak (problem.go
// sets Instance to c.Request.URL.Path verbatim), so it must not be scanned;
// everything the server composed itself still is.
//
// Scrubbing rather than decoding the body and scanning Problem.Title/Detail:
// a decode-based scan fails OPEN. If the response ever arrives as something
// other than a well-formed Problem — a middleware-written body, a truncated
// one — unmarshalling succeeds with empty fields and the scan passes on a body
// nobody looked at. This fails closed.
//
// The placeholder must be non-empty and must contain characters that appear in
// no scan token. An empty placeholder would splice the surrounding bytes
// together and could MANUFACTURE a match: "pg 235<id>05" would become
// "pg 23505". That is a correctness constraint, not cosmetics.
func leakScanTarget(body string, echoed ...string) string {
	for _, e := range echoed {
		if e == "" {
			continue
		}
		body = strings.ReplaceAll(body, e, "<echoed>")
	}
	return body
}
