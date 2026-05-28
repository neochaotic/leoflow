package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/neochaotic/leoflow/internal/domain"
)

// ErrAtCapacity is returned by BufferedDispatcher.Dispatch when the buffered
// queue cannot accept another request. The scheduler treats it exactly like a
// transient inner-dispatcher error: log + metric + leave the TI scheduled so
// the next tick re-tries. It is the backpressure signal that bounds tick
// latency under load (ADR 0031: tick rate decoupled from executor latency).
var ErrAtCapacity = errors.New("dispatch buffer at capacity; will retry next tick")

// Inner is the underlying synchronous dispatcher BufferedDispatcher wraps —
// matches scheduler.Dispatcher exactly so production wires through one type.
type Inner interface {
	Dispatch(ctx context.Context, runID, dagID string, task domain.TaskSpec) error
}

// FailureSink lets a worker report that an asynchronously-dispatched task
// failed inside the inner dispatcher, so the scheduler can fail the TI with a
// clear reason. Without this callback a `queued` TI whose dispatch failed
// would sit forever (no reaper targets `queued`; ADR 0031 #128 only targets
// `running`).
type FailureSink interface {
	MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error
}

// MetricsRecorder records dispatch-pool observability signals.
type MetricsRecorder interface {
	RecordDispatchQueueDepth(depth int)
	RecordDispatchAtCapacity()
	RecordDispatchLatencySeconds(seconds float64)
	RecordDispatchInnerError()
}

// BufferConfig sizes the BufferedDispatcher's worker pool. BufferSize=0 means
// "passthrough sync" (Lite mode, zero overhead): no goroutines spawned, no
// channel, the inner dispatcher is called inline. Any BufferSize>0 spawns
// max(Workers, 1) worker goroutines and a buffered channel of BufferSize
// slots.
type BufferConfig struct {
	BufferSize int
	Workers    int
}

// dispatchRequest carries one queued dispatch from the scheduler to a worker.
// runID, dagID, task are the same arguments the synchronous interface takes;
// ctx is the scheduler's caller context — the worker uses a derived
// background context so a cancellation of the caller does not abandon work
// the scheduler already considers "accepted".
type dispatchRequest struct {
	runID, dagID string
	task         domain.TaskSpec
}

// BufferedDispatcher fronts a synchronous Inner dispatcher with a bounded
// worker pool, so the scheduler tick is never blocked by a slow remote API
// call. ADR 0031: two-phase scheduler — planning sync, dispatch async.
type BufferedDispatcher struct {
	inner   Inner
	sink    FailureSink
	logger  *slog.Logger
	metrics MetricsRecorder
	cfg     BufferConfig
	queue   chan dispatchRequest
	wg      sync.WaitGroup
	closed  chan struct{}
	once    sync.Once
}

// NewBuffered constructs a BufferedDispatcher. BufferSize=0 returns a
// passthrough that is byte-for-byte equivalent to using the inner dispatcher
// directly (Lite path). BufferSize>0 spawns the worker pool.
func NewBuffered(inner Inner, sink FailureSink, logger *slog.Logger, metrics MetricsRecorder, cfg BufferConfig) *BufferedDispatcher {
	b := &BufferedDispatcher{
		inner:   inner,
		sink:    sink,
		logger:  logger,
		metrics: metrics,
		cfg:     cfg,
		closed:  make(chan struct{}),
	}
	if cfg.BufferSize <= 0 {
		return b
	}
	workers := cfg.Workers
	if workers <= 0 {
		workers = 1
	}
	b.queue = make(chan dispatchRequest, cfg.BufferSize)
	for i := 0; i < workers; i++ {
		b.wg.Add(1)
		go b.worker()
	}
	return b
}

// Dispatch hands a task off to the inner dispatcher. In passthrough mode the
// inner call happens inline. In buffered mode the request is enqueued non-
// blockingly: success returns nil immediately (the scheduler then records the
// TI as `queued`); a full channel returns ErrAtCapacity (the scheduler treats
// it as a transient failure and leaves the TI scheduled).
func (b *BufferedDispatcher) Dispatch(ctx context.Context, runID, dagID string, task domain.TaskSpec) error {
	if b.queue == nil {
		return b.inner.Dispatch(ctx, runID, dagID, task)
	}
	select {
	case b.queue <- dispatchRequest{runID: runID, dagID: dagID, task: task}:
		if b.metrics != nil {
			b.metrics.RecordDispatchQueueDepth(len(b.queue))
		}
		return nil
	default:
		if b.metrics != nil {
			b.metrics.RecordDispatchAtCapacity()
		}
		return ErrAtCapacity
	}
}

// Close stops accepting new dispatches, drains the in-flight queue, and waits
// for every worker to finish. Calling Close more than once is safe.
func (b *BufferedDispatcher) Close() {
	b.once.Do(func() {
		if b.queue != nil {
			close(b.queue)
		}
		close(b.closed)
	})
	b.wg.Wait()
}

// worker drains the queue, calling the inner dispatcher and reporting failures
// via the sink so the scheduler can fail the TI. A panic in the inner
// dispatcher is recovered so one poison task never kills the worker
// goroutine (ADR 0031 "the scheduler must never die" extends to the workers).
func (b *BufferedDispatcher) worker() {
	defer b.wg.Done()
	for req := range b.queue {
		b.dispatchOne(req)
	}
}

// dispatchOne calls the inner dispatcher with full panic isolation; a panic
// is reported via the sink as a dispatch failure so the scheduler can fail
// the TI rather than leaving it stuck `queued`.
func (b *BufferedDispatcher) dispatchOne(req dispatchRequest) {
	defer func() {
		if rec := recover(); rec != nil {
			b.logger.Error("dispatch worker panic recovered",
				"run", req.runID, "dag", req.dagID, "task", req.task.TaskID,
				"panic", rec, "stack", string(debug.Stack()))
			b.reportFailure(req, "dispatch_failed: worker panic")
		}
	}()
	// Use a background context: the caller's ctx may already be canceled by
	// the time the worker picks the request up (a long tick has rolled over),
	// but we already accepted responsibility for this dispatch — abandoning it
	// would leave a `queued` TI without a runner. The inner dispatcher's own
	// timeout protects against hanging.
	if err := b.inner.Dispatch(context.Background(), req.runID, req.dagID, req.task); err != nil { //nolint:contextcheck // worker intentionally detaches from the caller's ctx
		b.logger.Error("dispatch failed in worker",
			"run", req.runID, "dag", req.dagID, "task", req.task.TaskID, "error", err)
		if b.metrics != nil {
			b.metrics.RecordDispatchInnerError()
		}
		b.reportFailure(req, "dispatch_failed: "+err.Error())
	}
}

// reportFailure marks the TI failed via the sink if one is configured. A sink
// error is logged but cannot itself be propagated — the worker has already
// done all it can.
func (b *BufferedDispatcher) reportFailure(req dispatchRequest, reason string) {
	if b.sink == nil {
		return
	}
	if err := b.sink.MarkTaskDispatchFailed(context.Background(), req.runID, req.task.TaskID, reason); err != nil { //nolint:contextcheck // worker intentionally uses a fresh context for the failure report
		b.logger.Error("reporting dispatch failure",
			"run", req.runID, "task", req.task.TaskID, "error", err)
	}
}
