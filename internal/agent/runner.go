package agent

import (
	"bytes"
	"context"
	"encoding/json"
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

// rescheduleExitCode is the process exit code a reschedule-mode sensor uses to
// signal "not ready, re-dispatch me later" (it also writes the next-poke time to
// ReschedulePath). It mirrors RESCHEDULE_EXIT_CODE in the Python runtime; 75 is
// sysexits' EX_TEMPFAIL, distinct from the 0/non-zero success/failure mapping.
const rescheduleExitCode = 75

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
	LinksPath  string   // file the runtime writes operator_extra_links to; empty disables (#375)
	PushesPath string   // file the runtime writes custom-keyed XCom pushes to; empty disables (multi-key XCom)
	// ReschedulePath is the file a reschedule-mode sensor writes its next-poke time
	// to before exiting with rescheduleExitCode; empty disables reschedule (#380).
	ReschedulePath string
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
	argv, err := BuildCommand(spec.GetOperator(), spec.GetEntrypoint(), spec.GetOperatorClass())
	if err != nil {
		return err
	}
	env, err := r.buildEnv(ctx, spec)
	if err != nil {
		return err
	}
	return r.execute(ctx, argv, env, time.Duration(spec.GetExecutionTimeoutSeconds())*time.Second)
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
	for param, upstreams := range spec.GetXcomInputMapping() {
		taskIDs := upstreams.GetTaskIds()
		switch len(taskIDs) {
		case 0:
			// Empty upstream list — parser invariant prevents this, but guard anyway.
			continue
		case 1:
			// Single upstream: deliver the raw return_value JSON as-is, so a task
			// declaring `def f(x: dict)` receives the upstream's dict (not a
			// 1-element list wrapping it). Matches Airflow's TaskFlow semantics.
			resp, err := r.Client.FetchXCom(ctx, &agentv1.FetchXComRequest{
				UpstreamTaskId: taskIDs[0],
				Key:            "return_value",
			})
			if status.Code(err) == codes.NotFound {
				slog.Debug("declared xcom input is absent; leaving it unset", "param", param, "upstream", taskIDs[0])
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("fetching xcom %q from %q: %w", param, taskIDs[0], err)
			}
			xcom = append(xcom, XComEnvVar(param, resp.GetValue()))
		default:
			// Fan-in: each upstream's return_value becomes one element of a JSON
			// array, in declaration order. An absent upstream contributes `null`
			// so the function still receives len(upstreams) elements.
			collected, err := fetchFanInValues(ctx, r.Client, param, taskIDs)
			if err != nil {
				return nil, err
			}
			xcom = append(xcom, XComEnvVar(param, collected))
		}
	}
	env := mergeEnv(r.Env, spec.GetEnvironment(), xcom)
	// Force-unbuffered Python and explicit UTF-8 for every spawned subprocess
	// — including any python re-execed by the user's task. The `-u` flag on
	// argv (see BuildCommand) only covers the top-level interpreter; this env
	// var also flows to children. Without it, a user's print() bytes may sit
	// in Python's block buffer when the process is killed (SIGKILL/OOM/evict)
	// and never reach the agent's pipe.
	env = append(env, "PYTHONUNBUFFERED=1", "PYTHONIOENCODING=UTF-8")
	env = append(env, runContextEnv(spec)...)
	byTaskEnv, err := r.xcomByTaskEnv(ctx, spec)
	if err != nil {
		return nil, err
	}
	env = append(env, byTaskEnv...)
	if r.ReturnPath != "" {
		// Tell the runtime to write the return value to the agent's per-task path,
		// not the shared global default — so concurrent tasks and other users never
		// collide on /tmp/leoflow_return_value.json.
		env = append(env, "LEOFLOW_RETURN_VALUE_PATH="+r.ReturnPath)
	}
	if r.LinksPath != "" {
		// The runtime writes the operator's computed extra-links here (#375).
		env = append(env, "LEOFLOW_EXTRA_LINKS_PATH="+r.LinksPath)
	}
	if r.PushesPath != "" {
		// The runtime writes the operator's custom-keyed XCom pushes here (multi-key
		// XCom). Off the LEOFLOW_XCOM_ prefix so _merge_operator_xcom does not consume it.
		env = append(env, "LEOFLOW_PUSHES_PATH="+r.PushesPath)
	}
	if r.ReschedulePath != "" {
		// A reschedule-mode sensor writes its next-poke time here and exits with
		// rescheduleExitCode; the agent then reports up_for_reschedule (#380).
		env = append(env, "LEOFLOW_RESCHEDULE_PATH="+r.ReschedulePath)
	}
	if callArgs := spec.GetCallArgsJson(); callArgs != "" {
		// TaskFlow literal call args (#115). The runtime decodes this and merges
		// values into the user function's kwargs. XCom upstreams take precedence
		// at runtime for any same-name parameter. The env var name keeps
		// Airflow's DAG-run `params` term free for a future feature (#148).
		env = append(env, "LEOFLOW_CALL_ARGS_JSON="+callArgs)
	}
	if opArgs := spec.GetOperatorArgsJson(); opArgs != "" {
		// Operator constructor kwargs (ADR 0040). The runtime's --operator mode
		// decodes this and instantiates the captured provider operator with it.
		env = append(env, "LEOFLOW_OPERATOR_ARGS="+opArgs)
	}
	return append(env, r.secretsEnv(ctx)...), nil
}

// upstreamXComEnv is the env var carrying the upstream task_id -> return_value map the
// runtime's ti.xcom_pull reads. It deliberately does NOT start with "LEOFLOW_XCOM_":
// the runtime's _merge_operator_xcom consumes every LEOFLOW_XCOM_<PARAM> var as a
// param-bound operator kwarg and would otherwise inject this whole map as a bogus
// by_task= kwarg (a collision the live GCP chain test caught).
const upstreamXComEnv = "LEOFLOW_UPSTREAM_XCOM"

// xcomByTaskEnv fetches each upstream's return_value and renders the upstreamXComEnv
// map the runtime's ti.xcom_pull reads, so a captured operator can pull a chained
// upstream's output like in Airflow (ADR 0040). Only captured operators use
// ti.xcom_pull — a python @task gets its inputs via the param-keyed xcom_input_mapping
// — so the map is built for airflow_operator tasks only, avoiding wasted fetches. An
// upstream with no return_value is omitted (pulls as None). nil when nothing to deliver.
func (r *Runner) xcomByTaskEnv(ctx context.Context, spec *agentv1.TaskSpec) ([]string, error) {
	if spec.GetOperator() != "airflow_operator" {
		return nil, nil
	}
	byTask := map[string]json.RawMessage{}
	for _, taskID := range spec.GetDependsOn() {
		resp, err := r.Client.FetchXCom(ctx, &agentv1.FetchXComRequest{
			UpstreamTaskId: taskID,
			Key:            "return_value",
		})
		if status.Code(err) == codes.NotFound {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("fetching xcom for upstream %q: %w", taskID, err)
		}
		byTask[taskID] = resp.GetValue()
	}
	if len(byTask) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(byTask)
	if err != nil {
		return nil, fmt.Errorf("encoding xcom-by-task map: %w", err)
	}
	return []string{upstreamXComEnv + "=" + string(encoded)}, nil
}

// runContextEnv renders the DagRun/task identity the runtime's standalone operator
// context reads (_StandaloneTaskInstance / _operator_context, ADR 0040): without it
// the context is blank in every executor — ti.task_id="", run_id="", try_number
// defaulting to 1, and run_operator falling back to the operator class name as the
// task_id — a silent-wrong gap for operators that read them. The server carries
// these on the TaskSpec. ts/ds are derived from the single logical_date (ts is the
// RFC3339 value, ds its UTC date). The macros params/data_interval need fields not
// yet on TaskSpec and are deliberately left unset rather than fabricated.
func runContextEnv(spec *agentv1.TaskSpec) []string {
	var env []string
	if v := spec.GetTaskId(); v != "" {
		env = append(env, "LEOFLOW_TASK_ID="+v)
	}
	if v := spec.GetRunId(); v != "" {
		env = append(env, "LEOFLOW_RUN_ID="+v)
	}
	if v := spec.GetDagId(); v != "" {
		env = append(env, "LEOFLOW_DAG_ID="+v)
	}
	if n := spec.GetTryNumber(); n > 0 {
		env = append(env, fmt.Sprintf("LEOFLOW_TRY_NUMBER=%d", n))
	}
	if ld := spec.GetLogicalDate(); ld != "" {
		env = append(env, "LEOFLOW_TS="+ld)
		if t, perr := time.Parse(time.RFC3339, ld); perr == nil {
			env = append(env, "LEOFLOW_DS="+t.UTC().Format("2006-01-02"))
		}
	}
	if v := spec.GetDataIntervalStart(); v != "" {
		env = append(env, "LEOFLOW_DATA_INTERVAL_START="+v)
	}
	if v := spec.GetDataIntervalEnd(); v != "" {
		env = append(env, "LEOFLOW_DATA_INTERVAL_END="+v)
	}
	if v := spec.GetParamsJson(); v != "" {
		env = append(env, "LEOFLOW_PARAMS="+v)
	}
	if v := spec.GetFirstRescheduleAt(); v != "" {
		// Delivered to a re-dispatched reschedule-mode sensor so its
		// get_first_reschedule_date returns the real first time and `timeout` is
		// honored cumulatively across pokes (#380). Empty on the first attempt.
		env = append(env, "LEOFLOW_FIRST_RESCHEDULE_AT="+v)
	}
	return env
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

// fetchFanInValues fetches each upstream's return_value and assembles them into
// a JSON array, in declaration order. Each element is the raw JSON of that
// upstream's return value, or `null` if the upstream produced no XCom (Airflow
// semantics: missing XCom is None). The function the runtime calls receives
// this as `list[T]` — len(upstreams) elements, never fewer.
func fetchFanInValues(ctx context.Context, client agentv1.AgentServiceClient, param string, upstreams []string) ([]byte, error) {
	pieces := make([][]byte, 0, len(upstreams))
	for _, upstream := range upstreams {
		resp, err := client.FetchXCom(ctx, &agentv1.FetchXComRequest{
			UpstreamTaskId: upstream,
			Key:            "return_value",
		})
		if status.Code(err) == codes.NotFound {
			pieces = append(pieces, []byte("null"))
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("fan-in: fetching xcom %q from %q: %w", param, upstream, err)
		}
		// Each upstream's payload is already JSON; concat with commas inside `[…]`.
		pieces = append(pieces, resp.GetValue())
	}
	var b []byte
	b = append(b, '[')
	for i, p := range pieces {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, p...)
	}
	b = append(b, ']')
	return b, nil
}

func (r *Runner) execute(ctx context.Context, argv, env []string, timeout time.Duration) error {
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

	// `execution_timeout_seconds` enforcement (#194): wrap the runCtx in a
	// deadline so a wedged user process is interrupted at the boundary the user
	// declared, not at the 90 s heartbeat reaper. Zero (unset) preserves the
	// previous "no time bound" behavior. The deadline-fired vs heartbeat-canceled
	// vs parent-canceled cases are disambiguated below by inspecting
	// timeoutCtx.Err() after Cmd.Run returns.
	timeoutCtx := runCtx
	if timeout > 0 {
		var timeoutCancel context.CancelFunc
		timeoutCtx, timeoutCancel = context.WithTimeout(runCtx, timeout)
		defer timeoutCancel()
	}

	// Frame the run so a task with no print() still has visible logs in the UI —
	// matching real Airflow, which always emits start/end framing (#119).
	start := time.Now()
	emitTaskStarted(r.Sink)
	emitTaskBoot(r.Sink, argv, env)
	exitCode, runErr := r.Cmd.Run(timeoutCtx, argv, env, stdout, stderr)
	stdout.flush()
	stderr.flush()
	emitTaskEnded(r.Sink, exitCode, runErr, time.Since(start))
	if cerr := r.Sink.Close(); cerr != nil {
		slog.Warn("closing log stream", "error", cerr)
	}

	// Override the failure reason when the timeout fired. The check is on
	// timeoutCtx (not runCtx or ctx) so a heartbeat-driven cancel or a
	// parent SIGTERM still flows through the generic fail path.
	if timeout > 0 && errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		msg := fmt.Sprintf("execution_timeout: task exceeded %s limit", timeout)
		return r.failWithReason(ctx, exitCode, msg)
	}
	// A reschedule-mode sensor poked not-ready: it exits with rescheduleExitCode AND
	// leaves its next-poke time in ReschedulePath. The FILE is the real signal — a
	// user task that exits 75 (EX_TEMPFAIL) with no file is an ordinary failure, not
	// a reschedule (#386). Report up_for_reschedule only when both line up, before
	// the generic non-zero-exit path; otherwise fall through and fail normally.
	if runErr == nil && exitCode == rescheduleExitCode {
		if when, ok := r.readReschedule(); ok {
			return r.reportReschedule(ctx, when)
		}
	}
	if runErr != nil || exitCode != 0 {
		return r.fail(ctx, exitCode, runErr)
	}
	if err := r.pushReturnValue(ctx); err != nil {
		return r.fail(ctx, 0, err)
	}
	if err := r.pushExtraLinks(ctx); err != nil {
		return r.fail(ctx, 0, err)
	}
	if err := r.pushCustomXComs(ctx); err != nil {
		return r.fail(ctx, 0, err)
	}
	return r.report(ctx, agentv1.TaskState_TASK_STATE_SUCCESS, 0, "")
}

// failWithReason is fail() but with a pre-built error message — used by the
// execution_timeout path so the operator sees "timeout" rather than the
// generic "task exited non-zero" or the raw context.DeadlineExceeded.
func (r *Runner) failWithReason(ctx context.Context, exitCode int, msg string) error {
	if rerr := r.report(ctx, agentv1.TaskState_TASK_STATE_FAILED, clampExit(exitCode), msg); rerr != nil {
		slog.Warn("reporting failed state", "error", rerr)
	}
	return errors.New(msg)
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

// pushExtraLinks ships the operator's computed UI deep-link buttons (the runtime wrote
// them to LinksPath) to the control plane as the reserved "_extra_links" XCom, so the
// task Details view can render them (#375). Absent file or no-XCom control plane is a
// no-op — links are UI sugar and must not fail an otherwise-successful task.
func (r *Runner) pushExtraLinks(ctx context.Context) error {
	if r.LinksPath == "" {
		return nil
	}
	value, ok, err := ReadReturnValue(r.LinksPath)
	if err != nil || !ok {
		return err
	}
	resp, err := r.Client.PushXCom(ctx, &agentv1.PushXComRequest{
		Key:         "_extra_links",
		Value:       value,
		ContentType: "application/json",
	})
	if status.Code(err) == codes.Unimplemented {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pushing extra links: %w", err)
	}
	if !resp.GetAccepted() {
		return fmt.Errorf("control plane rejected extra links: %s", resp.GetRejectionReason())
	}
	return nil
}

// pushCustomXComs stores the operator's custom-keyed ti.xcom_push values (the runtime
// wrote them to PushesPath as a {key: value} JSON map) as individual XComs, so they
// show in the XCom tab like Airflow (multi-key XCom). Absent file or a no-XCom control
// plane is a no-op — these are observability, not task success.
func (r *Runner) pushCustomXComs(ctx context.Context) error {
	if r.PushesPath == "" {
		return nil
	}
	value, ok, err := ReadReturnValue(r.PushesPath)
	if err != nil || !ok {
		return err
	}
	var pushes map[string]json.RawMessage
	if uerr := json.Unmarshal(value, &pushes); uerr != nil {
		return fmt.Errorf("decoding xcom pushes: %w", uerr)
	}
	for _, key := range sortedKeys(pushes) {
		resp, perr := r.Client.PushXCom(ctx, &agentv1.PushXComRequest{
			Key:         key,
			Value:       pushes[key],
			ContentType: "application/json",
		})
		if status.Code(perr) == codes.Unimplemented {
			return nil
		}
		if perr != nil {
			return fmt.Errorf("pushing xcom %q: %w", key, perr)
		}
		if !resp.GetAccepted() {
			return fmt.Errorf("control plane rejected xcom %q: %s", key, resp.GetRejectionReason())
		}
	}
	return nil
}

// sortedKeys returns the map keys sorted, for deterministic push order.
func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
	return r.reportRequest(ctx, &agentv1.ReportStateRequest{
		State:        state,
		ExitCode:     exitCode,
		ErrorMessage: msg,
		OccurredAt:   timestamppb.Now(),
	})
}

// reportRequest sends a ReportState request and translates the response's
// should_terminate signal into an error.
func (r *Runner) reportRequest(ctx context.Context, req *agentv1.ReportStateRequest) error {
	resp, err := r.Client.ReportState(ctx, req)
	if err != nil {
		return fmt.Errorf("reporting state %v: %w", req.GetState(), err)
	}
	if resp.GetShouldTerminate() {
		return errors.New("control plane requested task termination")
	}
	return nil
}

// reschedule reads the next-poke time a reschedule-mode sensor wrote to
// ReschedulePath and reports up_for_reschedule with it, so the scheduler
// re-dispatches the task instance later without consuming retry budget (#380). A
// missing or unparseable time fails loudly rather than silently dropping the task.
// readReschedule reads and parses the next-poke time a reschedule-mode sensor wrote
// to ReschedulePath. ok is false when the path is unset, the file is absent, or it
// is unparseable — in which case the exit is treated as an ordinary result, not a
// reschedule, so exit 75 alone never hijacks a normal task outcome (#386).
func (r *Runner) readReschedule() (time.Time, bool) {
	if r.ReschedulePath == "" {
		return time.Time{}, false
	}
	data, ok, err := ReadReturnValue(r.ReschedulePath)
	if err != nil || !ok {
		return time.Time{}, false
	}
	when, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if perr != nil {
		return time.Time{}, false
	}
	return when, true
}

// reportReschedule reports up_for_reschedule with the next-poke time so the
// scheduler re-dispatches the task later, consuming no retry budget (#380).
func (r *Runner) reportReschedule(ctx context.Context, when time.Time) error {
	return r.reportRequest(ctx, &agentv1.ReportStateRequest{
		State:        agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE,
		OccurredAt:   timestamppb.Now(),
		RescheduleAt: timestamppb.New(when),
	})
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

// emitTaskBoot writes a lifecycle line summarizing what's about to run: the
// interpreter argv and the names of injected AIRFLOW_CONN_*, AIRFLOW_VAR_*, and
// LEOFLOW_XCOM_* env vars. Without this, a task with no print() shows only the
// 2-line framing — leaving the user wondering whether their conns/vars were
// injected or their kwargs resolved. Best-effort: a sink error is logged.
func emitTaskBoot(sink LogSink, argv, env []string) {
	if len(argv) == 0 {
		return
	}
	var conns, vars, xcoms []string
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "AIRFLOW_CONN_"):
			if eq := strings.IndexByte(kv, '='); eq > 0 {
				conns = append(conns, kv[:eq])
			}
		case strings.HasPrefix(kv, "AIRFLOW_VAR_"):
			if eq := strings.IndexByte(kv, '='); eq > 0 {
				vars = append(vars, kv[:eq])
			}
		case strings.HasPrefix(kv, "LEOFLOW_XCOM_"):
			if eq := strings.IndexByte(kv, '='); eq > 0 {
				xcoms = append(xcoms, kv[:eq])
			}
		}
	}
	sort.Strings(conns)
	sort.Strings(vars)
	sort.Strings(xcoms)
	msg := fmt.Sprintf("running: %s", strings.Join(argv, " "))
	if err := sink.Send(&agentv1.LogLine{
		Time: timestamppb.Now(), Level: agentv1.LogLevel_LOG_LEVEL_INFO,
		Message: msg, Stream: "agent",
	}); err != nil {
		slog.Warn("emitting task-boot log", "error", err)
	}
	if len(conns)+len(vars)+len(xcoms) == 0 {
		return
	}
	parts := make([]string, 0, 3)
	if len(conns) > 0 {
		parts = append(parts, fmt.Sprintf("conns: %s", strings.Join(conns, ", ")))
	}
	if len(vars) > 0 {
		parts = append(parts, fmt.Sprintf("vars: %s", strings.Join(vars, ", ")))
	}
	if len(xcoms) > 0 {
		parts = append(parts, fmt.Sprintf("xcom inputs: %s", strings.Join(xcoms, ", ")))
	}
	if err := sink.Send(&agentv1.LogLine{
		Time: timestamppb.Now(), Level: agentv1.LogLevel_LOG_LEVEL_INFO,
		Message: "injected " + strings.Join(parts, " | "), Stream: "agent",
	}); err != nil {
		slog.Warn("emitting task-boot env log", "error", err)
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
