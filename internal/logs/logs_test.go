package logs

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func ref() Ref {
	return Ref{TenantID: "acme", DagID: "etl", RunID: "run-1", TaskID: "extract", TryNumber: 1}
}

func TestDiskSinkWriteThenReadBack(t *testing.T) {
	dir := t.TempDir()
	sink := NewDiskSink(dir)

	w, err := sink.Open(ref())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, line := range []string{"first", "second"} {
		if werr := w.WriteLine(line); werr != nil {
			t.Fatalf("WriteLine: %v", werr)
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	// The file persists after the writer is closed (i.e. past pod termination).
	rc, err := sink.Read(ref())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "first\nsecond\n" {
		t.Errorf("read back %q, want \"first\\nsecond\\n\"", data)
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
