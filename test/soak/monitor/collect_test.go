package main

import (
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSplitMetricParsesTheExpositionFormat pins the hand-rolled scraper. The
// monitor parses three counters with a line scanner instead of pulling
// prometheus/common into the module's direct requirements, so the parsing is
// ours and has to be tested like anything else we wrote.
func TestSplitMetricParsesTheExpositionFormat(t *testing.T) {
	cases := []struct {
		line     string
		wantName string
		wantVal  float64
		wantOK   bool
	}{
		{"leoflow_scheduler_step_downs_total 3", "leoflow_scheduler_step_downs_total", 3, true},
		{`leoflow_scheduler_step_downs_total{reason="lost_lock"} 2`, "leoflow_scheduler_step_downs_total", 2, true},
		{`leoflow_tasks_undispatchable_total{reason="no executor",dag="x"} 1.5`, "leoflow_tasks_undispatchable_total", 1.5, true},
		{"leoflow_dispatch_queue_depth 0", "leoflow_dispatch_queue_depth", 0, true},
		{"# HELP leoflow_x help text", "", 0, false},
		{"nonsense", "", 0, false},
		{"leoflow_x not_a_number", "", 0, false},
		{"", "", 0, false},
	}
	for _, tc := range cases {
		name, val, ok := splitMetric(tc.line)
		if ok != tc.wantOK || name != tc.wantName || val != tc.wantVal {
			t.Errorf("splitMetric(%q) = (%q, %v, %v), want (%q, %v, %v)",
				tc.line, name, val, ok, tc.wantName, tc.wantVal, tc.wantOK)
		}
	}
}

// TestPercentilesUseNearestRank pins the quantile convention, which has to match
// test/load/scheduler_ceiling so the two harnesses' numbers can be compared.
func TestPercentilesUseNearestRank(t *testing.T) {
	p50, p95, p99, mx := pcts([]float64{5, 1, 4, 2, 3})
	if p50 != 3 {
		t.Errorf("p50 = %v, want 3", p50)
	}
	if p95 != 5 || p99 != 5 || mx != 5 {
		t.Errorf("p95/p99/max = %v/%v/%v, want 5/5/5", p95, p99, mx)
	}

	// An empty window must read as zero, not panic: the first samples of a soak
	// have no completed dispatch yet.
	if a, b, c, d := pcts(nil); a != 0 || b != 0 || c != 0 || d != 0 {
		t.Errorf("empty input = %v/%v/%v/%v, want zeros", a, b, c, d)
	}

	// The input must not be reordered: the caller reuses its slice for the
	// per-operator split.
	in := []float64{3, 1, 2}
	pcts(in)
	if in[0] != 3 || in[1] != 1 || in[2] != 2 {
		t.Errorf("pcts sorted its caller's slice: %v", in)
	}
}

// TestDirBytesSumsRegularFilesAndToleratesAMissingTree pins the disk-budget
// reader. It runs against a tree the DAGs are writing into, so a vanished entry
// has to be a skipped entry and never a failure.
func TestDirBytesSumsRegularFilesAndToleratesAMissingTree(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scratch"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.bin"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scratch", "b.bin"), make([]byte, 250), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dirBytes(root); got != 350 {
		t.Errorf("dirBytes = %d, want 350", got)
	}
	if got := dirBytes(filepath.Join(root, "does-not-exist")); got != 0 {
		t.Errorf("dirBytes of a missing tree = %d, want 0", got)
	}
}

// TestHumanBytesRendersTheSummaryColumn pins the one place a reader looks first,
// including the unknown case, which must not render as a number.
func TestHumanBytesRendersTheSummaryColumn(t *testing.T) {
	cases := map[int64]string{
		-1:              "unknown",
		0:               "0.0 B",
		1023:            "1023.0 B",
		1024:            "1.0 KiB",
		1024 * 1024:     "1.0 MiB",
		3 * 1024 * 1024: "3.0 MiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestFirstEnvAndEnvOrResolveConfiguration pins the flag fallbacks, which are the
// difference between a soak pointed at its own database and one pointed at the
// developer's.
func TestFirstEnvAndEnvOrResolveConfiguration(t *testing.T) {
	t.Setenv("SOAK_TEST_A", "")
	t.Setenv("SOAK_TEST_B", "second")
	if got := firstEnv("SOAK_TEST_A", "SOAK_TEST_B"); got != "second" {
		t.Errorf("firstEnv skipped an empty value incorrectly: %q", got)
	}
	if got := firstEnv("SOAK_TEST_MISSING"); got != "" {
		t.Errorf("firstEnv of a missing key = %q, want empty", got)
	}
	if got := envOr("SOAK_TEST_MISSING", "fallback"); got != "fallback" {
		t.Errorf("envOr = %q, want the fallback", got)
	}
	if got := envOr("SOAK_TEST_B", "fallback"); got != "second" {
		t.Errorf("envOr overrode a set value: %q", got)
	}
}

// TestCountersReportNaNWhenUnreachable pins that a failed scrape is distinguished
// from a genuine zero. Reading an unreachable /metrics as zero would make the
// leader-churn and undispatchable checks silently unfalsifiable.
func TestCountersReportNaNWhenUnreachable(t *testing.T) {
	c := &collector{
		o:    options{apiURL: "http://127.0.0.1:1"},
		http: &http.Client{Timeout: time.Second},
	}
	a, b, d := c.counters(t.Context())
	if !math.IsNaN(a) || !math.IsNaN(b) || !math.IsNaN(d) {
		t.Errorf("counters against an unreachable endpoint = %v/%v/%v, want NaN", a, b, d)
	}
}
