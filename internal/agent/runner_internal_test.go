package agent

import (
	"errors"
	"strings"
	"testing"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

func TestClampExit(t *testing.T) {
	cases := map[int]int32{
		0: 0, 1: 1, 137: 137, 255: 255,
		-1: 255, -255: 255, // negative is out of range -> clamp
		256: 255, 4096: 255, // above a byte -> clamp
	}
	for in, want := range cases {
		if got := clampExit(in); got != want {
			t.Errorf("clampExit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestMergeEnvSortsSpecDeterministically(t *testing.T) {
	got := mergeEnv(
		[]string{"BASE=1"},
		map[string]string{"ZED": "26", "ALPHA": "1", "MID": "13"},
		[]string{"XCOM=x"},
	)
	want := []string{"BASE=1", "ALPHA=1", "MID=13", "ZED=26", "XCOM=x"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("mergeEnv = %v, want %v (base, sorted spec, then xcom)", got, want)
	}
	// Empty spec/xcom just returns the base.
	if g := mergeEnv([]string{"A=1"}, nil, nil); len(g) != 1 || g[0] != "A=1" {
		t.Errorf("mergeEnv with empty spec/xcom = %v", g)
	}
}

// capSink records full log lines (message + line number).
type capSink struct {
	msgs  []string
	lines []int64
}

func (s *capSink) Send(l *agentv1.LogLine) error {
	s.msgs = append(s.msgs, l.GetMessage())
	s.lines = append(s.lines, l.GetLineNumber())
	return nil
}
func (s *capSink) Close() error { return nil }

func TestLogWriterSplitsLines(t *testing.T) {
	sink := &capSink{}
	w := &logWriter{sink: sink, stream: "stdout", level: agentv1.LogLevel_LOG_LEVEL_INFO}

	// Two complete lines in one write, plus a partial line with no newline.
	n, err := w.Write([]byte("first\nsecond\npart"))
	if err != nil || n != len("first\nsecond\npart") {
		t.Fatalf("Write returned n=%d err=%v", n, err)
	}
	if strings.Join(sink.msgs, "|") != "first|second" {
		t.Errorf("complete lines should emit immediately, got %v", sink.msgs)
	}
	// A second write completes the partial line.
	if _, err := w.Write([]byte("ial\n")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sink.msgs, "|") != "first|second|partial" {
		t.Errorf("partial line should complete across writes, got %v", sink.msgs)
	}
	// Line numbers are monotonic from 1.
	for i, ln := range sink.lines {
		if ln != int64(i+1) {
			t.Errorf("line %d numbered %d, want %d", i, ln, i+1)
		}
	}
}

func TestLogWriterFlushEmitsTrailingPartial(t *testing.T) {
	sink := &capSink{}
	w := &logWriter{sink: sink}
	if _, err := w.Write([]byte("no newline here")); err != nil {
		t.Fatal(err)
	}
	if len(sink.msgs) != 0 {
		t.Errorf("a partial line must not emit until flush/newline, got %v", sink.msgs)
	}
	w.flush()
	if len(sink.msgs) != 1 || sink.msgs[0] != "no newline here" {
		t.Errorf("flush should emit the buffered remainder, got %v", sink.msgs)
	}
	// Flushing again with an empty buffer is a no-op.
	w.flush()
	if len(sink.msgs) != 1 {
		t.Errorf("flushing an empty buffer should do nothing, got %v", sink.msgs)
	}
}

// errSink fails every Send, simulating a broken log stream.
type errSink struct{ sends int }

func (s *errSink) Send(*agentv1.LogLine) error { s.sends++; return errors.New("stream broken") }
func (s *errSink) Close() error                { return nil }

// TestLogWriterSurvivesSinkErrors breaks the log sink: a streaming failure must
// not break the writer (the task keeps running; logs are best-effort).
func TestLogWriterSurvivesSinkErrors(t *testing.T) {
	sink := &errSink{}
	w := &logWriter{sink: sink}
	if _, err := w.Write([]byte("a\nb\n")); err != nil {
		t.Fatalf("Write must not surface sink errors, got %v", err)
	}
	w.flush()
	if sink.sends != 2 {
		t.Errorf("both lines should have been attempted, got %d sends", sink.sends)
	}
}
