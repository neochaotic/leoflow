package logs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory ObjectStore for exercising ObjectSink without a real
// bucket. It records every Put so tests can assert the key layout.
type memStore struct {
	mu     sync.Mutex
	objs   map[string][]byte
	putErr error
	getErr error
}

func newMemStore() *memStore { return &memStore{objs: map[string][]byte{}} }

func (m *memStore) Put(_ context.Context, key string, r io.Reader) error {
	if m.putErr != nil {
		return m.putErr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = b
	return nil
}

func (m *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func sampleRef() Ref {
	return Ref{TenantID: "t1", DagID: "dag", RunID: "scheduled__2026-07-30T12:00:00+00:00", TaskID: "task", TryNumber: 1}
}

func TestObjectSinkRoundTrip(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "logs")
	ref := sampleRef()

	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	events := []Event{
		{Level: "info", Stream: "stdout", Message: "starting"},
		{Level: "error", Stream: "stderr", Message: "boom"},
	}
	for _, ev := range events {
		if werr := w.WriteEvent(ev); werr != nil {
			t.Fatalf("WriteEvent() error = %v", werr)
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}

	rc, err := sink.Read(ref)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != len(events) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(events), string(raw))
	}
	first := DecodeLine(lines[0])
	if first.Message != "starting" || first.Level != "info" {
		t.Errorf("first line = %+v, want starting/info", first)
	}
	second := DecodeLine(lines[1])
	if second.Message != "boom" || second.Level != "error" {
		t.Errorf("second line = %+v, want boom/error", second)
	}
}

func TestObjectSinkKeyLayoutIncludesPrefix(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "acme/logs")
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if werr := w.WriteEvent(Event{Message: "x"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	wantKey := "acme/logs/t1/dag/scheduled__2026-07-30T12:00:00+00:00/task/1.log"
	if _, ok := store.objs[wantKey]; !ok {
		keys := make([]string, 0, len(store.objs))
		for k := range store.objs {
			keys = append(keys, k)
		}
		t.Fatalf("object not stored at %q; got keys %v", wantKey, keys)
	}
}

func TestObjectSinkRejectsUnsafeRef(t *testing.T) {
	sink := NewObjectSink(context.Background(), newMemStore(), "")
	bad := sampleRef()
	bad.RunID = "../escape"
	if _, err := sink.Open(bad); !errors.Is(err, ErrUnsafeRef) {
		t.Fatalf("Open(unsafe) error = %v, want ErrUnsafeRef", err)
	}
	if _, err := sink.Read(bad); !errors.Is(err, ErrUnsafeRef) {
		t.Fatalf("Read(unsafe) error = %v, want ErrUnsafeRef", err)
	}
}

func TestObjectSinkReadMissing(t *testing.T) {
	sink := NewObjectSink(context.Background(), newMemStore(), "")
	if _, err := sink.Read(sampleRef()); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("Read(missing) error = %v, want ErrObjectNotFound", err)
	}
}

func TestObjectSinkClosePropagatesPutError(t *testing.T) {
	store := newMemStore()
	store.putErr = errors.New("network down")
	sink := NewObjectSink(context.Background(), store, "")
	w, err := sink.Open(sampleRef())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if werr := w.WriteEvent(Event{Message: "x"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	if cerr := w.Close(); cerr == nil {
		t.Fatal("Close() error = nil, want the underlying put error")
	}
}

func TestDiskSinkImplementsPrunerObjectSinkDoesNot(t *testing.T) {
	var disk Sink = NewDiskSink(t.TempDir())
	if _, ok := disk.(Pruner); !ok {
		t.Error("DiskSink should implement Pruner")
	}
	var obj Sink = NewObjectSink(context.Background(), newMemStore(), "")
	if _, ok := obj.(Pruner); ok {
		t.Error("ObjectSink must not implement Pruner (bucket lifecycle owns retention)")
	}
}

func TestNewDurableSinkDefaultsToDisk(t *testing.T) {
	dir := t.TempDir()
	for _, backend := range []string{"", "disk"} {
		sink, err := NewDurableSink(context.Background(), backend, dir, nil, "")
		if err != nil {
			t.Fatalf("NewDurableSink(%q) error = %v", backend, err)
		}
		if _, ok := sink.(*DiskSink); !ok {
			t.Errorf("NewDurableSink(%q) = %T, want *DiskSink", backend, sink)
		}
	}
}

func TestNewDurableSinkObject(t *testing.T) {
	sink, err := NewDurableSink(context.Background(), "object", "", newMemStore(), "p")
	if err != nil {
		t.Fatalf("NewDurableSink(object) error = %v", err)
	}
	if _, ok := sink.(*ObjectSink); !ok {
		t.Errorf("NewDurableSink(object) = %T, want *ObjectSink", sink)
	}
}

func TestNewDurableSinkObjectRequiresStore(t *testing.T) {
	if _, err := NewDurableSink(context.Background(), "object", "", nil, ""); err == nil {
		t.Fatal("NewDurableSink(object, nil store) error = nil, want error")
	}
}

func TestNewDurableSinkUnknownBackend(t *testing.T) {
	if _, err := NewDurableSink(context.Background(), "gopher", "", nil, ""); err == nil {
		t.Fatal("NewDurableSink(unknown) error = nil, want error")
	}
}

// keep time imported for future dated assertions without churn.
var _ = time.Now
