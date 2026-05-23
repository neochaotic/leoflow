package executor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/logs"
	"github.com/neochaotic/leoflow/internal/xcom"
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

// XComPusher stores an inline task's output as an XCom value.
type XComPusher interface {
	Push(ctx context.Context, key xcom.Key, value []byte, contentType string, schema map[string]any) error
}

// InlineConfig bundles an InlineRunner's dependencies. XCom and Logs are
// optional; when nil, the corresponding shipping is skipped.
type InlineConfig struct {
	Sink        StateSink
	Metrics     InlineMetrics
	XCom        XComPusher
	Logs        logs.Sink
	Concurrency int
	MaxSeconds  int
	UserAgent   string
}

// InlineRunner executes http_api tasks declared with execution_mode: inline as
// goroutines in the control plane, bounded by a concurrency semaphore and a
// per-task duration cap (ADR 0002). On success it ships the response body as the
// task's return_value XCom and writes a summary log line.
type InlineRunner struct {
	exec      func(ctx context.Context, req Request) ([]byte, error)
	sink      StateSink
	metrics   InlineMetrics
	xcom      XComPusher
	logs      logs.Sink
	sem       chan struct{}
	maxSecs   int
	userAgent string
	wg        sync.WaitGroup
}

// NewInlineRunner builds an InlineRunner from the given config.
func NewInlineRunner(cfg InlineConfig) *InlineRunner {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	return &InlineRunner{
		exec:      NewInlineHTTPExecutor(nil, 0).Run,
		sink:      cfg.Sink,
		metrics:   cfg.Metrics,
		xcom:      cfg.XCom,
		logs:      cfg.Logs,
		sem:       make(chan struct{}, cfg.Concurrency),
		maxSecs:   cfg.MaxSeconds,
		userAgent: cfg.UserAgent,
	}
}

// inlineTask carries the full identity an inline run needs for state, XCom, and
// log shipping.
type inlineTask struct {
	runID     string
	dagID     string
	tenantID  string
	tryNumber int
	spec      domain.TaskSpec
}

// Start begins inline execution of task. It returns (false, nil) when the
// concurrency limit is reached so the scheduler retries on the next tick, and
// (false, error) when the task is invalid for inline execution (timeout above
// the cap). On success it marks the task running, launches the work, and
// returns (true, nil).
func (r *InlineRunner) Start(ctx context.Context, runID, dagID, tenantID string, tryNumber int, task domain.TaskSpec) (bool, error) {
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
	go r.run(context.WithoutCancel(ctx), inlineTask{runID, dagID, tenantID, tryNumber, task})
	return true, nil
}

// Wait blocks until every in-flight inline task has finished. It is used for
// graceful shutdown and by tests.
func (r *InlineRunner) Wait() { r.wg.Wait() }

// run executes the task, ships its output and logs, and records its terminal
// state. It always releases the semaphore and the wait group.
func (r *InlineRunner) run(ctx context.Context, it inlineTask) {
	defer r.wg.Done()
	defer func() { <-r.sem }()

	start := time.Now()
	state, body := r.execute(ctx, it.spec)

	if state == domain.TaskStateSuccess {
		r.shipXCom(ctx, it, body)
	}
	r.shipLog(ctx, it, state, time.Since(start))

	if r.metrics != nil {
		r.metrics.RecordTaskTransition(string(domain.TaskStateRunning), string(state), it.dagID)
		r.metrics.RecordTaskDuration(it.dagID, it.spec.TaskID, string(it.spec.Type), time.Since(start).Seconds())
	}

	writeCtx, cancel := context.WithTimeout(ctx, inlineTerminalWriteTimeout)
	defer cancel()
	if err := r.sink.Transition(writeCtx, it.runID, it.spec.TaskID, state); err != nil {
		slog.Error("recording inline task terminal state", "run", it.runID, "task", it.spec.TaskID, "error", err)
	}
}

// execute performs the HTTP call under a timeout, recovering from panics so a
// misbehaving task fails cleanly instead of crashing the control plane. It
// returns the terminal state and, on success, the response body.
func (r *InlineRunner) execute(ctx context.Context, task domain.TaskSpec) (state domain.TaskState, body []byte) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("inline task panic", "task", task.TaskID, "panic", rec)
			state, body = domain.TaskStateFailed, nil
		}
	}()

	timeout := r.maxSecs
	if t := task.ExecutionTimeoutSeconds; t != nil && *t > 0 {
		timeout = *t
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	req := Request{Operator: "http_api", TaskID: task.TaskID, HTTPRequest: r.withUserAgent(task.HTTPRequest)}
	out, err := r.exec(callCtx, req)
	if err != nil {
		slog.Warn("inline http_api task failed", "task", task.TaskID, "error", err)
		return domain.TaskStateFailed, nil
	}
	return domain.TaskStateSuccess, out
}

// shipXCom stores a non-empty response body as the task's return_value XCom.
// It is best-effort: a push failure (e.g. oversize) is logged, not fatal, since
// the task itself already succeeded.
func (r *InlineRunner) shipXCom(ctx context.Context, it inlineTask, body []byte) {
	if r.xcom == nil || len(body) == 0 {
		return
	}
	key := xcom.Key{TenantID: it.tenantID, DagID: it.dagID, RunID: it.runID, TaskID: it.spec.TaskID, Name: "return_value"}
	pctx, cancel := context.WithTimeout(ctx, inlineTerminalWriteTimeout)
	defer cancel()
	if err := r.xcom.Push(pctx, key, body, "application/json", it.spec.XComSchema); err != nil {
		slog.Warn("pushing inline xcom", "task", it.spec.TaskID, "error", err)
	}
}

// shipLog writes a one-line summary of the inline call to the log sink so the
// task has logs in the UI like pod tasks do.
func (r *InlineRunner) shipLog(ctx context.Context, it inlineTask, state domain.TaskState, dur time.Duration) {
	if r.logs == nil || it.spec.HTTPRequest == nil {
		return
	}
	w, err := r.logs.Open(logs.Ref{
		TenantID: it.tenantID, DagID: it.dagID, RunID: it.runID, TaskID: it.spec.TaskID, TryNumber: it.tryNumber,
	})
	if err != nil {
		slog.Warn("opening inline log sink", "task", it.spec.TaskID, "error", err)
		return
	}
	line := fmt.Sprintf("inline http_api %s %s -> %s (%.2fs)", it.spec.HTTPRequest.Method, it.spec.HTTPRequest.URL, state, dur.Seconds())
	level := "info"
	if state == domain.TaskStateFailed {
		level = "error"
	}
	if werr := w.WriteEvent(logs.Event{Level: level, Stream: "stdout", Message: line}); werr != nil {
		slog.Warn("writing inline log", "task", it.spec.TaskID, "error", werr)
	}
	if cerr := w.Close(); cerr != nil {
		slog.Warn("closing inline log", "task", it.spec.TaskID, "error", cerr)
	}
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
