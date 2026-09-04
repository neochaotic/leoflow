package logs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a concurrency-safe io.Writer for a test slog handler: the
// object writer logs from both WriteEvent's caller and the flusher goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// testLogger returns a logger and the buffer it writes into, so a test can
// assert on what the sink logged through its INJECTED logger rather than on
// whatever the process default happens to be.
func testLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// newFailingStore is a memStore whose every Put fails with msg.
func newFailingStore(msg string) *memStore {
	store := newMemStore()
	store.setPutErr(errors.New(msg))
	return store
}

// TestObjectSinkFlushFailureUsesInjectedLogger pins the observability half of
// the incremental-flush path: the warning must travel through the logger the
// process configured, not Go's package default. leoflow-server never calls
// slog.SetDefault, so a warning left on the default handler is plain text on
// stderr, outside the configured handler and outside its level — which means a
// failing flush is invisible to whatever collects the control plane's logs, and
// a validation drill that sees no flush errors proves nothing.
func TestObjectSinkFlushFailureUsesInjectedLogger(t *testing.T) {
	setFlushTuning(t, 8, time.Hour)
	logger, buf := testLogger()
	sink := NewObjectSink(context.Background(), newFailingStore("503 slow down"), "", logger)
	w, err := sink.Open(sampleRef())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// A line past the (lowered) size threshold triggers an incremental flush, which
	// the store fails.
	if werr := w.WriteEvent(Event{Message: "a line long enough to trip the flush threshold"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v; an incremental flush failure must not end the stream", werr)
	}

	got := buf.String()
	if !strings.Contains(got, "incremental log object flush failed") {
		t.Fatalf("injected logger recorded %q; want the incremental-flush warning to travel through the configured logger, not Go's default handler", got)
	}
	if !strings.Contains(got, "503 slow down") {
		t.Fatalf("injected logger recorded %q; want the store error included so the failure is diagnosable", got)
	}
}

// TestNewDurableSinkCarriesTheLoggerToTheObjectSink pins the PRODUCTION hand-off,
// which is the only place the injection actually has to happen: leoflow-server
// never calls NewObjectSink itself, it calls NewDurableSink. Every other test in
// this package passes nil, so dropping the logger on the way through
// NewDurableSink — NewObjectSink(ctx, store, prefix, nil) — kept the whole
// package green while putting the warning straight back on Go's default handler,
// which is the bug the injection exists to fix. Assert on the injected buffer,
// reached only through the constructor the server uses.
func TestNewDurableSinkCarriesTheLoggerToTheObjectSink(t *testing.T) {
	setFlushTuning(t, 8, time.Hour)
	logger, buf := testLogger()
	sink, err := NewDurableSink(context.Background(), "s3", "", newFailingStore("503 slow down"), "", logger)
	if err != nil {
		t.Fatalf("NewDurableSink(s3) error = %v", err)
	}
	w, err := sink.Open(sampleRef())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if werr := w.WriteEvent(Event{Message: "a line long enough to trip the flush threshold"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v; an incremental flush failure must not end the stream", werr)
	}

	if got := buf.String(); !strings.Contains(got, "incremental log object flush failed") {
		t.Fatalf("a sink built through NewDurableSink logged %q into the injected logger; want the incremental-flush warning, or the server's logger is being dropped on the way to the object sink", got)
	}
}

// TestObjectSinkNilLoggerFallsBackToDefault: a nil logger is legal and means
// "use the package default", so an embedder (or a test) that does not care
// about log routing keeps working instead of panicking on the first failed
// flush.
func TestObjectSinkNilLoggerFallsBackToDefault(t *testing.T) {
	setFlushTuning(t, 8, time.Hour)
	sink := NewObjectSink(context.Background(), newFailingStore("503 slow down"), "", nil)
	w, err := sink.Open(sampleRef())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if werr := w.WriteEvent(Event{Message: "a line long enough to trip the flush threshold"}); werr != nil {
		t.Fatalf("WriteEvent() with a nil logger error = %v", werr)
	}
}
