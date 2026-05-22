package executor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
)

// inlineTerminalWriteTimeout bounds the background write of an inline task's
// terminal state, which runs after the scheduler tick context may be gone.
const inlineTerminalWriteTimeout = 30 * time.Second

// StateSink records the state transitions of an inline task.
type StateSink interface {
	Transition(ctx context.Context, runID, taskID string, state domain.TaskState) error
}

// InlineMetrics records inline task execution metrics.
type InlineMetrics interface {
	RecordTaskTransition(from, to, dagID string)
	RecordTaskDuration(dagID, taskID, taskType string, seconds float64)
}

// InlineRunner executes http_api tasks declared with execution_mode: inline as
// goroutines in the control plane, bounded by a concurrency semaphore and a
// per-task duration cap (ADR 0002).
type InlineRunner struct {
	exec      func(ctx context.Context, req Request) error
	sink      StateSink
	metrics   InlineMetrics
	sem       chan struct{}
	maxSecs   int
	userAgent string
	wg        sync.WaitGroup
}

// NewInlineRunner builds an InlineRunner that writes state through sink, records
// metrics (optional, may be nil), admits at most concurrency tasks at once, caps
// each task at maxSecs seconds, and sends userAgent on every request.
func NewInlineRunner(sink StateSink, metrics InlineMetrics, concurrency, maxSecs int, userAgent string) *InlineRunner {
	if concurrency < 1 {
		concurrency = 1
	}
	return &InlineRunner{
		exec:      NewInlineHTTPExecutor(nil, 0).Execute,
		sink:      sink,
		metrics:   metrics,
		sem:       make(chan struct{}, concurrency),
		maxSecs:   maxSecs,
		userAgent: userAgent,
	}
}

// Start begins inline execution of task. It returns (false, nil) when the
// concurrency limit is reached so the scheduler retries on the next tick, and
// (false, error) when the task is invalid for inline execution (timeout above
// the cap). On success it marks the task running, launches the work, and
// returns (true, nil).
func (r *InlineRunner) Start(ctx context.Context, runID, dagID string, task domain.TaskSpec) (bool, error) {
	if t := task.ExecutionTimeoutSeconds; t != nil && *t > r.maxSecs {
		return false, fmt.Errorf(
			"inline http_api task %q timeout (%ds) exceeds server limit (%ds); declare execution_mode: pod",
			task.TaskID, *t, r.maxSecs)
	}
	select {
	case r.sem <- struct{}{}:
	default:
		return false, nil
	}
	if err := r.sink.Transition(ctx, runID, task.TaskID, domain.TaskStateRunning); err != nil {
		<-r.sem
		return false, fmt.Errorf("marking task %q running: %w", task.TaskID, err)
	}
	r.wg.Add(1)
	// The task intentionally outlives the scheduler tick: WithoutCancel keeps
	// trace values but detaches cancellation, so a tick-boundary cancellation
	// cannot abort an in-flight call or its terminal-state write (ADR 0002).
	go r.run(context.WithoutCancel(ctx), runID, dagID, task)
	return true, nil
}

// Wait blocks until every in-flight inline task has finished. It is used for
// graceful shutdown and by tests.
func (r *InlineRunner) Wait() { r.wg.Wait() }

// run executes the task and records its terminal state. It always releases the
// semaphore and the wait group.
func (r *InlineRunner) run(ctx context.Context, runID, dagID string, task domain.TaskSpec) {
	defer r.wg.Done()
	defer func() { <-r.sem }()

	start := time.Now()
	state := r.execute(ctx, task)

	if r.metrics != nil {
		r.metrics.RecordTaskTransition(string(domain.TaskStateRunning), string(state), dagID)
		r.metrics.RecordTaskDuration(dagID, task.TaskID, string(task.Type), time.Since(start).Seconds())
	}

	writeCtx, cancel := context.WithTimeout(ctx, inlineTerminalWriteTimeout)
	defer cancel()
	if err := r.sink.Transition(writeCtx, runID, task.TaskID, state); err != nil {
		slog.Error("recording inline task terminal state", "run", runID, "task", task.TaskID, "error", err)
	}
}

// execute performs the HTTP call under a timeout, recovering from panics so a
// misbehaving task fails cleanly instead of crashing the control plane.
func (r *InlineRunner) execute(ctx context.Context, task domain.TaskSpec) (state domain.TaskState) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("inline task panic", "task", task.TaskID, "panic", rec)
			state = domain.TaskStateFailed
		}
	}()

	timeout := r.maxSecs
	if t := task.ExecutionTimeoutSeconds; t != nil && *t > 0 {
		timeout = *t
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	req := Request{Operator: "http_api", TaskID: task.TaskID, HTTPRequest: r.withUserAgent(task.HTTPRequest)}
	if err := r.exec(callCtx, req); err != nil {
		slog.Warn("inline http_api task failed", "task", task.TaskID, "error", err)
		return domain.TaskStateFailed
	}
	return domain.TaskStateSuccess
}

// withUserAgent returns a copy of req with the configured User-Agent header set
// when one is not already present.
func (r *InlineRunner) withUserAgent(req *domain.HTTPRequest) *domain.HTTPRequest {
	if req == nil {
		return nil
	}
	out := *req
	out.Headers = make(map[string]string, len(req.Headers)+1)
	for k, v := range req.Headers {
		out.Headers[k] = v
	}
	if _, ok := out.Headers["User-Agent"]; !ok && r.userAgent != "" {
		out.Headers["User-Agent"] = r.userAgent
	}
	return &out
}
