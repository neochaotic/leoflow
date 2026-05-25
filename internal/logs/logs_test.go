package logs

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ref() Ref {
	return Ref{TenantID: "acme", DagID: "etl", RunID: "run-1", TaskID: "extract", TryNumber: 1}
}

func TestRefChannel(t *testing.T) {
	if got := ref().Channel(); got != "log_tail:acme:etl:run-1:extract:1" {
		t.Errorf("Channel() = %q", got)
	}
}

func TestDiskSinkWriteThenReadBack(t *testing.T) {
	dir := t.TempDir()
	sink := NewDiskSink(dir)

	w, err := sink.Open(ref())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, line := range []string{"first", "second"} {
		if werr := w.WriteEvent(Event{Level: "info", Stream: "stdout", Message: line}); werr != nil {
			t.Fatalf("WriteEvent: %v", werr)
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	// The file persists after the writer is closed (i.e. past pod termination)
	// and is stored as JSONL; decoding each line recovers the messages.
	rc, err := sink.Read(ref())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	rawLines := strings.Split(strings.TrimSpace(string(data)), "\n")
	msgs := make([]string, 0, len(rawLines))
	for _, raw := range rawLines {
		msgs = append(msgs, DecodeLine(raw).Message)
	}
	if len(msgs) != 2 || msgs[0] != "first" || msgs[1] != "second" {
		t.Errorf("read back %q -> messages %v, want [first second]", data, msgs)
	}
}

func TestDecodeLineRoundTripAndLegacy(t *testing.T) {
	// JSONL round-trip preserves the real level/stream.
	dir := t.TempDir()
	w, err := NewDiskSink(dir).Open(ref())
	if err != nil {
		t.Fatal(err)
	}
	if werr := w.WriteEvent(Event{Level: "error", Stream: "stderr", Message: "boom"}); werr != nil {
		t.Fatal(werr)
	}
	_ = w.Close()
	rc, _ := NewDiskSink(dir).Read(ref())
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	ev := DecodeLine(strings.TrimSpace(string(data)))
	if ev.Level != "error" || ev.Stream != "stderr" || ev.Message != "boom" {
		t.Errorf("round-trip lost fields: %+v", ev)
	}

	// Legacy plain lines decode as stdout with an inferred level.
	legacy := DecodeLine("ERROR something failed")
	if legacy.Level != "error" || legacy.Message != "ERROR something failed" {
		t.Errorf("legacy decode = %+v", legacy)
	}
	if DecodeLine("just a line").Level != "info" {
		t.Errorf("plain line should infer info")
	}
}

func TestDiskSinkPathLayout(t *testing.T) {
	dir := t.TempDir()
	sink := NewDiskSink(dir)
	w, err := sink.Open(ref())
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	want := filepath.Join(dir, "acme", "etl", "run-1", "extract", "1.log")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected log at %s: %v", want, err)
	}
}

func TestDiskSinkReadMissing(t *testing.T) {
	if _, err := NewDiskSink(t.TempDir()).Read(ref()); err == nil {
		t.Error("reading a non-existent log should error")
	}
}

func TestDiskSinkPruneDeletesOldLogs(t *testing.T) {
	dir := t.TempDir()
	sink := NewDiskSink(dir)
	w, err := sink.Open(ref())
	if err != nil {
		t.Fatal(err)
	}
	_ = w.WriteEvent(Event{Message: "x"})
	_ = w.Close()

	now := time.Now()
	logPath := filepath.Join(dir, "acme", "etl", "run-1", "extract", "1.log")
	old := now.Add(-40 * 24 * time.Hour)
	if cerr := os.Chtimes(logPath, old, old); cerr != nil {
		t.Fatal(cerr)
	}

	if perr := sink.Prune(now, 30*24*time.Hour); perr != nil {
		t.Fatalf("Prune: %v", perr)
	}
	if _, serr := os.Stat(logPath); !os.IsNotExist(serr) {
		t.Errorf("log older than retention should be pruned (stat err = %v)", serr)
	}
}

func TestDiskSinkPruneKeepsRecentAndMissingRoot(t *testing.T) {
	dir := t.TempDir()
	sink := NewDiskSink(dir)
	w, _ := sink.Open(ref())
	_ = w.Close()
	if err := sink.Prune(time.Now(), 30*24*time.Hour); err != nil {
		t.Fatalf("Prune recent: %v", err)
	}
	if _, err := sink.Read(ref()); err != nil {
		t.Errorf("recent log should be kept: %v", err)
	}
	// A never-written root must not error.
	if err := NewDiskSink(filepath.Join(dir, "nope")).Prune(time.Now(), time.Hour); err != nil {
		t.Errorf("pruning a missing root should be a no-op, got %v", err)
	}
}

func TestRefineLevel(t *testing.T) {
	cases := []struct {
		msg, fallback, want string
	}{
		// Content with a clear token wins over the stream-derived fallback.
		{"ERROR: connection refused", "info", "error"}, // error printed to stdout
		{"Traceback (most recent call last):", "info", "error"},
		{"CRITICAL boom", "info", "error"},
		{"INFO: started run", "error", "info"}, // info written to stderr
		{"WARNING: deprecated API", "error", "warning"},
		{"DEBUG cache hit", "info", "debug"},
		// No recognizable token -> the stream-derived fallback stands.
		{"processed 42 rows", "info", "info"},
		{"raw stderr noise", "error", "error"},
		{"", "info", "info"},
	}
	for _, c := range cases {
		if got := RefineLevel(c.msg, c.fallback); got != c.want {
			t.Errorf("RefineLevel(%q, %q) = %q, want %q", c.msg, c.fallback, got, c.want)
		}
	}
}

func TestInferLevelDefaultsToInfo(t *testing.T) {
	if inferLevel("nothing notable here") != "info" {
		t.Error("a line with no severity token should infer info")
	}
	if inferLevel("WARNING: heads up") != "warning" {
		t.Error("a WARNING line should infer warning")
	}
}
