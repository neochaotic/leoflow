package dispatch_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/dispatch"
	"github.com/neochaotic/leoflow/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingInner is a scheduler.Dispatcher fake that records every call. It is
// the dispatcher the BufferedDispatcher wraps in tests.
type recordingInner struct {
	mu        sync.Mutex
	calls     []string
	err       error
	delay     time.Duration
	callCount atomic.Int64
}

func (r *recordingInner) Dispatch(_ context.Context, _, _ string, task domain.TaskSpec) error {
	r.callCount.Add(1)
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	r.calls = append(r.calls, task.TaskID)
	r.mu.Unlock()
	return r.err
}

func (r *recordingInner) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// recordingSink records dispatch-failure callbacks the worker makes when the
// inner dispatcher returns an error in async mode.
type recordingSink struct {
	mu      sync.Mutex
	failed  []failedDispatch
	failErr error
}

type failedDispatch struct {
	runID, taskID, reason string
}

func (r *recordingSink) MarkTaskDispatchFailed(_ context.Context, runID, taskID, reason string) error {
	r.mu.Lock()
	r.failed = append(r.failed, failedDispatch{runID, taskID, reason})
	r.mu.Unlock()
	return r.failErr
}

func (r *recordingSink) snapshot() []failedDispatch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]failedDispatch(nil), r.failed...)
}

// TestBuffered_Passthrough_BufferSizeZero pins the Lite contract: BufferSize=0
// means no goroutines, no channel — the call goes straight to the inner
// dispatcher and the result (including errors) bubbles up unchanged. This is
// the zero-overhead path Lite relies on.
func TestBuffered_Passthrough_BufferSizeZero(t *testing.T) {
	inner := &recordingInner{}
	d := dispatch.NewBuffered(inner, nil, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 0, Workers: 0})
	defer d.Close()
	err := d.Dispatch(context.Background(), "r1", "etl", domain.TaskSpec{TaskID: "extract"})
	if err != nil {
		t.Fatalf("passthrough err = %v", err)
	}
	if got := inner.seen(); len(got) != 1 || got[0] != "extract" {
		t.Errorf("inner saw %v, want [extract]", got)
	}
}

// TestBuffered_Passthrough_PropagatesInnerError: in Lite mode an inner error
// surfaces directly — same shape as the original sync dispatcher contract.
func TestBuffered_Passthrough_PropagatesInnerError(t *testing.T) {
	innerErr := errors.New("kube down")
	inner := &recordingInner{err: innerErr}
	d := dispatch.NewBuffered(inner, nil, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 0, Workers: 0})
	defer d.Close()
	if err := d.Dispatch(context.Background(), "r1", "etl", domain.TaskSpec{TaskID: "t"}); !errors.Is(err, innerErr) {
		t.Errorf("passthrough err = %v, want wrap of innerErr", err)
	}
}

// TestBuffered_Async_AcceptsAndDrains: BufferSize>0 returns immediately
// (the dispatcher's contract from the scheduler's POV: nil = "I have accepted
// responsibility for this task") and workers drain the channel into the
// inner dispatcher.
func TestBuffered_Async_AcceptsAndDrains(t *testing.T) {
	inner := &recordingInner{}
	d := dispatch.NewBuffered(inner, &recordingSink{}, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 4, Workers: 2})
	defer d.Close()
	for _, id := range []string{"a", "b", "c"} {
		if err := d.Dispatch(context.Background(), "r1", "etl", domain.TaskSpec{TaskID: id}); err != nil {
			t.Fatalf("Dispatch %s: %v", id, err)
		}
	}
	// Wait for the inner dispatcher to see all three (workers run async).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && inner.callCount.Load() < 3 {
		time.Sleep(5 * time.Millisecond)
	}
	if inner.callCount.Load() != 3 {
		t.Errorf("inner saw %d calls, want 3", inner.callCount.Load())
	}
}

// TestBuffered_Async_BackpressureWhenChannelFull is the load-bearing
// backpressure contract: when the channel cannot accept a new request,
// Dispatch returns ErrAtCapacity. The scheduler treats this exactly like a
// transient inner-dispatcher error: log + metric + leave TI as scheduled so
// the next tick re-tries. This is what bounds tick latency under load.
func TestBuffered_Async_BackpressureWhenChannelFull(t *testing.T) {
	// One slow worker, channel-of-one — the second Dispatch must fail fast
	// because the first is blocking the only worker and the channel is full.
	inner := &recordingInner{delay: 200 * time.Millisecond}
	d := dispatch.NewBuffered(inner, &recordingSink{}, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 1, Workers: 1})
	defer d.Close()

	if err := d.Dispatch(context.Background(), "r1", "etl", domain.TaskSpec{TaskID: "slow"}); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}
	// At least one more must end up "at capacity": the worker is asleep on the
	// first call, the channel has only one slot, and the test floods the queue.
	hitCapacity := false
	for i := 0; i < 10 && !hitCapacity; i++ {
		err := d.Dispatch(context.Background(), "r1", "etl", domain.TaskSpec{TaskID: "flood"})
		if errors.Is(err, dispatch.ErrAtCapacity) {
			hitCapacity = true
		}
	}
	if !hitCapacity {
		t.Errorf("expected at least one Dispatch to return ErrAtCapacity under flood")
	}
}

// TestBuffered_Async_WorkerFailureMarksTIFailed: when the inner dispatcher
// errors, the worker calls back via the sink so the scheduler observes the
// failure (and the TI is failed with reason 'dispatch_failed: ...' — the
// retry budget governs retry, matching Airflow's KubernetesExecutor).
// Without this callback the TI would sit `queued` forever (no reaper catches
// a queued TI; ADR 0031 #128 only targets `running`).
func TestBuffered_Async_WorkerFailureMarksTIFailed(t *testing.T) {
	innerErr := errors.New("kube create pod refused")
	inner := &recordingInner{err: innerErr}
	sink := &recordingSink{}
	d := dispatch.NewBuffered(inner, sink, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 4, Workers: 1})
	defer d.Close()

	if err := d.Dispatch(context.Background(), "r1", "etl", domain.TaskSpec{TaskID: "doomed"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(sink.snapshot()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	got := sink.snapshot()
	if len(got) != 1 || got[0].taskID != "doomed" || got[0].runID != "r1" {
		t.Errorf("sink failed = %+v, want one failure for r1/doomed", got)
	}
}

// TestBuffered_Async_PanicInInnerDoesNotCrash is the resilience pin: a panic
// in the wrapped dispatcher (a bug, a malformed task, anything) must not
// crash the worker goroutine. The worker recovers, the failure is reported
// via the sink, and the pool keeps draining.
func TestBuffered_Async_PanicInInnerDoesNotCrash(t *testing.T) {
	panicker := &panicInner{}
	sink := &recordingSink{}
	d := dispatch.NewBuffered(panicker, sink, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 2, Workers: 1})
	defer d.Close()

	if err := d.Dispatch(context.Background(), "r1", "etl", domain.TaskSpec{TaskID: "boom"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(sink.snapshot()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sink.snapshot(); len(got) != 1 || got[0].taskID != "boom" {
		t.Errorf("panic must be reported via sink, got %+v", got)
	}
}

type panicInner struct{}

func (panicInner) Dispatch(context.Context, string, string, domain.TaskSpec) error {
	panic("boom: inner dispatcher")
}

// TestBuffered_Close_DrainsPendingWork: Close blocks until in-flight workers
// finish, so a shutdown does not abandon a task that the scheduler already
// recorded as `queued`. After Close returns, every accepted Dispatch is
// either delivered to the inner dispatcher or reported failed via the sink.
func TestBuffered_Close_DrainsPendingWork(t *testing.T) {
	inner := &recordingInner{}
	d := dispatch.NewBuffered(inner, &recordingSink{}, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 8, Workers: 2})
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := d.Dispatch(context.Background(), "r1", "etl", domain.TaskSpec{TaskID: id}); err != nil {
			t.Fatalf("Dispatch %s: %v", id, err)
		}
	}
	d.Close()
	if inner.callCount.Load() != 4 {
		t.Errorf("after Close, inner saw %d calls, want 4 (every accepted Dispatch must drain)", inner.callCount.Load())
	}
}
