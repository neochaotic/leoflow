package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CommandRunner executes the user task process, writing its stdout and stderr to
// the supplied writers and returning the process exit code.
type CommandRunner interface {
	Run(ctx context.Context, argv, env []string, stdout, stderr io.Writer) (exitCode int, err error)
}

// LogSink receives log lines produced by the user task. Sends are best-effort.
type LogSink interface {
	Send(line *agentv1.LogLine) error
	Close() error
}

// NoopLogSink discards log lines. The agent falls back to it when the control
// plane log stream is unavailable (e.g. StreamLogs not yet implemented), so a
// task still runs even though its logs are not shipped this run.
type NoopLogSink struct{}

// Send discards the line.
func (NoopLogSink) Send(*agentv1.LogLine) error { return nil }

// Close is a no-op.
func (NoopLogSink) Close() error { return nil }

// Runner orchestrates a single task execution inside the worker container: it
// registers with the control plane, fetches the task spec and XCom inputs, runs
// the user process while streaming logs, pushes the return value, and reports the
// terminal state.
type Runner struct {
	Client     agentv1.AgentServiceClient
	Cmd        CommandRunner
	Sink       LogSink
	Hostname   string
	Version    string
	Env        []string // base process environment (typically os.Environ())
	ReturnPath string   // file the task writes its return value to; empty disables push
	// HeartbeatInterval is how often to ping the control plane while the task
	// runs; zero disables heartbeats.
	HeartbeatInterval time.Duration
}

// Run executes the task lifecycle and returns an error if the task failed.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.register(ctx); err != nil {
		return err
	}
	spec, err := r.Client.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{})
	if err != nil {
		return fmt.Errorf("fetching task spec: %w", err)
	}
	if spec.GetOperator() == "http_api" {
		return errors.New("agent received an http_api task, which is executed by the control plane")
	}
	argv, err := BuildCommand(spec.GetOperator(), spec.GetEntrypoint())
	if err != nil {
		return err
	}
	env, err := r.buildEnv(ctx, spec)
	if err != nil {
		return err
	}
	return r.execute(ctx, argv, env)
}

func (r *Runner) register(ctx context.Context) error {
	if _, err := r.Client.Register(ctx, &agentv1.RegisterRequest{
		AgentVersion: r.Version,
		Hostname:     r.Hostname,
	}); err != nil {
		return fmt.Errorf("registering agent: %w", err)
	}
	return nil
}

func (r *Runner) buildEnv(ctx context.Context, spec *agentv1.TaskSpec) ([]string, error) {
	var xcom []string
	for param, upstream := range spec.GetXcomInputMapping() {
		resp, err := r.Client.FetchXCom(ctx, &agentv1.FetchXComRequest{
			UpstreamTaskId: upstream,
			Key:            "return_value",
		})
		if status.Code(err) == codes.NotFound {
			// Airflow semantics: a missing XCom resolves to None, so skip the
			// input rather than failing the task.
			slog.Debug("declared xcom input is absent; leaving it unset", "param", param, "upstream", upstream)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("fetching xcom %q from %q: %w", param, upstream, err)
		}
		xcom = append(xcom, XComEnvVar(param, resp.GetValue()))
	}
	env := mergeEnv(r.Env, spec.GetEnvironment(), xcom)
	if r.ReturnPath != "" {
		// Tell the runtime to write the return value to the agent's per-task path,
		// not the shared global default — so concurrent tasks and other users never
		// collide on /tmp/leoflow_return_value.json.
		env = append(env, "LEOFLOW_RETURN_VALUE_PATH="+r.ReturnPath)
	}
	if callArgs := spec.GetCallArgsJson(); callArgs != "" {
		// TaskFlow literal call args (#115). The runtime decodes this and merges
		// values into the user function's kwargs. XCom upstreams take precedence
		// at runtime for any same-name parameter. The env var name keeps
		// Airflow's DAG-run `params` term free for a future feature (#148).
		env = append(env, "LEOFLOW_CALL_ARGS_JSON="+callArgs)
	}
	return append(env, r.secretsEnv(ctx)...), nil
}

// secretsEnv fetches the tenant's Variables/Connections and renders them as
// AIRFLOW_VAR_<KEY> / AIRFLOW_CONN_<ID> so Airflow's native env secrets backend
// (and plain os.environ) resolve them (ADR 0021). Best-effort: a fetch failure
// (e.g. an insecure channel refusing secrets) logs and is skipped, so tasks that
// do not use Variables/Connections still run.
func (r *Runner) secretsEnv(ctx context.Context) []string {
	var out []string
	if resp, err := r.Client.GetVariables(ctx, &agentv1.GetVariablesRequest{}); err != nil {
		slog.Warn("fetching variables; Variable.get may be unavailable", "error", err)
	} else {
		for k, v := range resp.GetVariables() {
			out = append(out, "AIRFLOW_VAR_"+strings.ToUpper(k)+"="+v)
		}
	}
	if resp, err := r.Client.GetConnections(ctx, &agentv1.GetConnectionsRequest{}); err != nil {
		slog.Warn("fetching connections; get_connection may be unavailable", "error", err)
	} else {
		for id, uri := range resp.GetConnectionUris() {
			out = append(out, "AIRFLOW_CONN_"+strings.ToUpper(id)+"="+uri)
		}
	}
	return out
}

func (r *Runner) execute(ctx context.Context, argv, env []string) error {
	if err := r.report(ctx, agentv1.TaskState_TASK_STATE_RUNNING, 0, ""); err != nil {
		return err
	}
	stdout := &logWriter{sink: r.Sink, stream: "stdout", level: agentv1.LogLevel_LOG_LEVEL_INFO}
	stderr := &logWriter{sink: r.Sink, stream: "stderr", level: agentv1.LogLevel_LOG_LEVEL_ERROR}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if r.HeartbeatInterval > 0 {
		go r.heartbeat(runCtx, cancel)
	}

	// Frame the run so a task with no print() still has visible logs in the UI —
	// matching real Airflow, which always emits start/end framing (#119).
	start := time.Now()
	emitTaskStarted(r.Sink)
	exitCode, runErr := r.Cmd.Run(runCtx, argv, env, stdout, stderr)
	stdout.flush()
	stderr.flush()
	emitTaskEnded(r.Sink, exitCode, runErr, time.Since(start))
	if cerr := r.Sink.Close(); cerr != nil {
		slog.Warn("closing log stream", "error", cerr)
	}

	if runErr != nil || exitCode != 0 {
		return r.fail(ctx, exitCode, runErr)
	}
	if err := r.pushReturnValue(ctx); err != nil {
		return r.fail(ctx, 0, err)
	}
	return r.report(ctx, agentv1.TaskState_TASK_STATE_SUCCESS, 0, "")
}

func (r *Runner) fail(ctx context.Context, exitCode int, cause error) error {
	msg := "task exited non-zero"
	if cause != nil {
		msg = cause.Error()
	}
	if rerr := r.report(ctx, agentv1.TaskState_TASK_STATE_FAILED, clampExit(exitCode), msg); rerr != nil {
		slog.Warn("reporting failed state", "error", rerr)
	}
	if cause != nil {
		return fmt.Errorf("task failed (exit %d): %w", exitCode, cause)
	}
	return fmt.Errorf("task failed with exit code %d", exitCode)
}

func (r *Runner) pushReturnValue(ctx context.Context) error {
	if r.ReturnPath == "" {
		return nil
	}
	value, ok, err := ReadReturnValue(r.ReturnPath)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	resp, err := r.Client.PushXCom(ctx, &agentv1.PushXComRequest{
		Key:         "return_value",
		Value:       value,
		ContentType: "application/json",
	})
	if status.Code(err) == codes.Unimplemented {
		// XCom persistence lands in Phase 4; until then a return value is not
		// stored, but that must not fail an otherwise-successful task.
		slog.Warn("control plane does not implement XCom yet; dropping return value")
		return nil
	}
	if err != nil {
		return fmt.Errorf("pushing return value: %w", err)
	}
	if !resp.GetAccepted() {
		return fmt.Errorf("control plane rejected return value: %s", resp.GetRejectionReason())
	}
	return nil
}

// heartbeat pings the control plane on an interval while the task runs and
// cancels it when the control plane signals termination.
func (r *Runner) heartbeat(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(r.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			resp, err := r.Client.Heartbeat(ctx, &agentv1.HeartbeatRequest{SentAt: timestamppb.Now()})
			if err != nil {
				slog.Warn("heartbeat failed", "error", err)
				continue
			}
			if resp.GetShouldTerminate() {
				slog.Warn("control plane requested task termination")
				cancel()
				return
			}
		}
	}
}

func (r *Runner) report(ctx context.Context, state agentv1.TaskState, exitCode int32, msg string) error {
	resp, err := r.Client.ReportState(ctx, &agentv1.ReportStateRequest{
		State:        state,
		ExitCode:     exitCode,
		ErrorMessage: msg,
		OccurredAt:   timestamppb.Now(),
	})
	if err != nil {
		return fmt.Errorf("reporting state %v: %w", state, err)
	}
	if resp.GetShouldTerminate() {
		return errors.New("control plane requested task termination")
	}
	return nil
}

// mergeEnv combines the base environment with the task spec variables (sorted for
// determinism) and the fetched XCom input variables.
func mergeEnv(base []string, spec map[string]string, xcom []string) []string {
	out := make([]string, 0, len(base)+len(spec)+len(xcom))
	out = append(out, base...)
	keys := make([]string, 0, len(spec))
	for k := range spec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+spec[k])
	}
	return append(out, xcom...)
}

// clampExit narrows an OS exit code to the byte range a process can return.
func clampExit(code int) int32 {
	if code < 0 || code > 255 {
		return 255
	}
	return int32(code)
}

// logWriter splits written bytes into newline-delimited log lines and forwards
// each one to the sink, tagging it with its stream name and level.
type logWriter struct {
	sink   LogSink
	stream string
	level  agentv1.LogLevel
	buf    []byte
	line   int64
}

// Write buffers p and emits every complete line it contains.
func (w *logWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// flush emits any buffered line that lacked a trailing newline.
func (w *logWriter) flush() {
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
}

// emitTaskStarted writes a synthetic start-of-task event to the log sink, so the
// UI's Logs panel always shows at least one line — even for a task that calls no
// print(). Best-effort; a sink error is logged but not propagated (the task
// itself is what matters).
func emitTaskStarted(sink LogSink) {
	if err := sink.Send(&agentv1.LogLine{
		Time:    timestamppb.Now(),
		Level:   agentv1.LogLevel_LOG_LEVEL_INFO,
		Message: "▸ task started",
		Stream:  "agent",
	}); err != nil {
		slog.Warn("emitting task-started log", "error", err)
	}
}

// emitTaskEnded writes a synthetic end-of-task event with the run duration and
// either a success marker or the exit code + cause. Pairs with emitTaskStarted to
// guarantee the Logs panel is never empty for a completed task (#119).
func emitTaskEnded(sink LogSink, exitCode int, cause error, duration time.Duration) {
	d := duration.Round(time.Millisecond)
	var msg string
	level := agentv1.LogLevel_LOG_LEVEL_INFO
	if cause == nil && exitCode == 0 {
		msg = fmt.Sprintf("✓ task succeeded in %s", d)
	} else {
		level = agentv1.LogLevel_LOG_LEVEL_ERROR
		if cause != nil {
			msg = fmt.Sprintf("✗ task failed (exit %d) in %s: %s", exitCode, d, cause.Error())
		} else {
			msg = fmt.Sprintf("✗ task failed (exit %d) in %s", exitCode, d)
		}
	}
	if err := sink.Send(&agentv1.LogLine{
		Time:    timestamppb.Now(),
		Level:   level,
		Message: msg,
		Stream:  "agent",
	}); err != nil {
		slog.Warn("emitting task-ended log", "error", err)
	}
}

func (w *logWriter) emit(b []byte) {
	w.line++
	if err := w.sink.Send(&agentv1.LogLine{
		Time:       timestamppb.Now(),
		Level:      w.level,
		Message:    string(b),
		Stream:     w.stream,
		LineNumber: w.line,
	}); err != nil {
		slog.Warn("streaming log line", "stream", w.stream, "error", err)
	}
}
