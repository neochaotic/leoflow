package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestASampleWithAnUnscrapedCounterIsStillWritten is the evidence-integrity
// case. encoding/json refuses NaN, and the counters are NaN exactly when the
// metrics listener could not be scraped, which is exactly during the incident
// the soak exists to record. A sample that cannot be marshaled is dropped
// silently by appendJSON and turns verdict.json into "{}", so the outage window
// would be the one window with no evidence in it.
func TestASampleWithAnUnscrapedCounterIsStillWritten(t *testing.T) {
	dir := t.TempDir()
	rep, err := newReporter(options{outDir: dir, label: "nan"})
	if err != nil {
		t.Fatalf("newReporter: %v", err)
	}
	defer rep.Close()

	s := sample{Seq: 1, SchedulerHealth: "unreachable"}
	s.StepDowns, s.Undispatchable, s.DispatchAtCap = nanMetric(), nanMetric(), nanMetric()
	rep.WriteSample(s, nil)
	v := rep.Finish("duration_reached", map[string]int{})
	if v.Samples != 1 {
		t.Fatalf("reporter counted %d samples", v.Samples)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "samples.jsonl"))
	if err != nil {
		t.Fatalf("reading samples.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("the sample was dropped: samples.jsonl = %q", string(raw))
	}
	var back map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &back); uerr != nil {
		t.Fatalf("the written sample is not valid JSON: %v", uerr)
	}
	if got, ok := back["scheduler_step_downs_total"]; !ok || got != nil {
		t.Errorf("an unscraped counter serialized as %v, want JSON null so a reader can tell it from zero", got)
	}

	vraw, err := os.ReadFile(filepath.Join(dir, "verdict.json"))
	if err != nil {
		t.Fatalf("reading verdict.json: %v", err)
	}
	var vb map[string]any
	if uerr := json.Unmarshal(vraw, &vb); uerr != nil {
		t.Fatalf("verdict.json is not valid JSON: %v", uerr)
	}
	if vb["verdict"] == nil {
		t.Fatalf("verdict.json lost its verdict when a counter was unscraped: %s", string(vraw))
	}
}

// TestUnscrapedCounterRoundTripsAsNotANumber pins the in-process semantics the
// checks rely on: an unscraped counter must still be distinguishable from zero
// after it has been through the sample struct.
func TestUnscrapedCounterRoundTripsAsNotANumber(t *testing.T) {
	m := nanMetric()
	if !math.IsNaN(float64(m)) {
		t.Fatalf("nanMetric() = %v, want NaN", float64(m))
	}
	c := newChecker(defaultOptions())
	s := healthySample()
	s.StepDowns, s.Undispatchable = m, m
	if vs := c.Check(s); len(vs) != 0 {
		t.Errorf("an unscraped counter produced %v", checkNames(vs))
	}
}
