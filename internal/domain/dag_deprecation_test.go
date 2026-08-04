package domain

import (
	"strings"
	"testing"
)

// TestDeprecationWarnings pins the http_api deprecation surface (ADR 0047): a
// registered spec containing an http_api task must yield a warning naming the
// task and pointing to HttpOperator, so `leoflow push` can show it. Other task
// types yield nothing. The warning is the one-release courtesy before http_api
// is rejected outright and the inline executor removed (issue #512).
func TestDeprecationWarnings(t *testing.T) {
	spec := &DAGSpec{
		SchemaVersion: "1.0", DagID: "d", DagVersion: "v1", Image: "img:v1",
		Tasks: []TaskSpec{
			{TaskID: "fetch", Type: TaskTypeHTTPAPI},
			{TaskID: "run", Type: TaskTypePython},
		},
	}
	warns := spec.DeprecationWarnings()
	if len(warns) != 1 {
		t.Fatalf("want 1 warning (the http_api task), got %d: %v", len(warns), warns)
	}
	w := warns[0]
	for _, want := range []string{"fetch", "http_api", "deprecated", "HttpOperator"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning missing %q: %s", want, w)
		}
	}

	clean := &DAGSpec{Tasks: []TaskSpec{{TaskID: "run", Type: TaskTypePython}}}
	if got := clean.DeprecationWarnings(); len(got) != 0 {
		t.Errorf("a spec with no http_api task must yield no warnings, got %v", got)
	}
}
