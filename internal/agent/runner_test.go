package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeClient is a test double for the generated AgentServiceClient.
type fakeClient struct {
	spec               *agentv1.TaskSpec
	xcom               map[string]*agentv1.FetchXComResponse
	states             []agentv1.TaskState
	reports            []*agentv1.ReportStateRequest
	pushed             []*agentv1.PushXComRequest
	registered         bool
	terminateAt        agentv1.TaskState // state for which ReportState returns should_terminate
	getSpecErr         error
	pushErr            error
	heartbeatTerminate bool
	vars               map[string]string
	conns              map[string]string
}

func (f *fakeClient) GetVariables(context.Context, *agentv1.GetVariablesRequest, ...grpc.CallOption) (*agentv1.GetVariablesResponse, error) {
	return &agentv1.GetVariablesResponse{Variables: f.vars}, nil
}

func (f *fakeClient) GetConnections(context.Context, *agentv1.GetConnectionsRequest, ...grpc.CallOption) (*agentv1.GetConnectionsResponse, error) {
	return &agentv1.GetConnectionsResponse{ConnectionUris: f.conns}, nil
}

func (f *fakeClient) Register(context.Context, *agentv1.RegisterRequest, ...grpc.CallOption) (*agentv1.RegisterResponse, error) {
	f.registered = true
	return &agentv1.RegisterResponse{SessionId: "s1"}, nil
}

func (f *fakeClient) GetTaskSpec(context.Context, *agentv1.GetTaskSpecRequest, ...grpc.CallOption) (*agentv1.TaskSpec, error) {
	if f.getSpecErr != nil {
		return nil, f.getSpecErr
	}
	return f.spec, nil
}

func (f *fakeClient) FetchXCom(_ context.Context, in *agentv1.FetchXComRequest, _ ...grpc.CallOption) (*agentv1.FetchXComResponse, error) {
	if resp, ok := f.xcom[in.GetUpstreamTaskId()]; ok {
		return resp, nil
	}
	return nil, status.Error(codes.NotFound, "no xcom for "+in.GetUpstreamTaskId())
}

func (f *fakeClient) PushXCom(_ context.Context, in *agentv1.PushXComRequest, _ ...grpc.CallOption) (*agentv1.PushXComResponse, error) {
	f.pushed = append(f.pushed, in)
	if f.pushErr != nil {
		return nil, f.pushErr
	}
	return &agentv1.PushXComResponse{Accepted: true}, nil
}

func (f *fakeClient) StreamLogs(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[agentv1.LogLine, agentv1.LogAck], error) {
	return nil, errors.New("not used in these tests")
}

func (f *fakeClient) ReportState(_ context.Context, in *agentv1.ReportStateRequest, _ ...grpc.CallOption) (*agentv1.ReportStateResponse, error) {
	f.states = append(f.states, in.GetState())
	f.reports = append(f.reports, in)
	return &agentv1.ReportStateResponse{Acknowledged: true, ShouldTerminate: in.GetState() == f.terminateAt}, nil
}

func (f *fakeClient) Heartbeat(context.Context, *agentv1.HeartbeatRequest, ...grpc.CallOption) (*agentv1.HeartbeatResponse, error) {
	return &agentv1.HeartbeatResponse{ShouldTerminate: f.heartbeatTerminate}, nil
}

// recordingSink captures log lines instead of streaming them.
type recordingSink struct {
	lines  []string
	closed bool
}

func (s *recordingSink) Send(line *agentv1.LogLine) error {
	s.lines = append(s.lines, line.GetMessage())
	return nil
}
func (s *recordingSink) Close() error { s.closed = true; return nil }

// fakeCmd is a CommandRunner double that records its inputs and emits output.
type fakeCmd struct {
	argv             []string
	env              []string
	stdout           string
	exitCode         int
	err              error
	blockUntilCancel bool
}

func (c *fakeCmd) Run(ctx context.Context, argv, env []string, stdout, _ io.Writer) (int, error) {
	c.argv, c.env = argv, env
	if c.stdout != "" {
		_, _ = io.WriteString(stdout, c.stdout)
	}
	if c.blockUntilCancel {
		<-ctx.Done()
		return 137, ctx.Err()
	}
	return c.exitCode, c.err
}

func newRunner(client *fakeClient, cmd *fakeCmd, sink *recordingSink) *Runner {
	return &Runner{
		Client:   client,
		Cmd:      cmd,
		Sink:     sink,
		Hostname: "pod-1",
		Version:  "test",
	}
}

func TestRunnerHappyPath(t *testing.T) {
	client := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator:         "python",
			Entrypoint:       "dag:hello",
			Environment:      map[string]string{"FOO": "bar"},
			XcomInputMapping: map[string]string{"upstream_val": "extract"},
		},
		xcom: map[string]*agentv1.FetchXComResponse{
			"extract": {Value: []byte(`{"n":1}`)},
		},
	}
	cmd := &fakeCmd{stdout: "line one\nline two\n"}
	sink := &recordingSink{}
	r := newRunner(client, cmd, sink)

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !client.registered {
		t.Error("agent should register on startup")
	}
	wantStates := []agentv1.TaskState{agentv1.TaskState_TASK_STATE_RUNNING, agentv1.TaskState_TASK_STATE_SUCCESS}
	if len(client.states) != 2 || client.states[0] != wantStates[0] || client.states[1] != wantStates[1] {
		t.Errorf("states = %v, want running then success", client.states)
	}
	if cmd.argv[0] != "python" {
		t.Errorf("argv = %v, want python command", cmd.argv)
	}
	joined := strings.Join(cmd.env, "\n")
	if !strings.Contains(joined, "FOO=bar") {
		t.Errorf("env missing spec var: %v", cmd.env)
	}
	if !strings.Contains(joined, `LEOFLOW_XCOM_UPSTREAM_VAL={"n":1}`) {
		t.Errorf("env missing xcom input: %v", cmd.env)
	}
	// Variables/Connections are exported as Airflow env secrets.
	client.vars = map[string]string{"my_var": "v1"}
	client.conns = map[string]string{"my_db": "postgres://u:p@h/db"}
	env, err := r.buildEnv(context.Background(), client.spec)
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	je := strings.Join(env, "\n")
	if !strings.Contains(je, "AIRFLOW_VAR_MY_VAR=v1") {
		t.Errorf("env missing AIRFLOW_VAR_MY_VAR: %v", env)
	}
	if !strings.Contains(je, "AIRFLOW_CONN_MY_DB=postgres://u:p@h/db") {
		t.Errorf("env missing AIRFLOW_CONN_MY_DB: %v", env)
	}
	// The agent frames a run with synthetic start/end events (#119) and emits
	// task-boot lifecycle lines (running:, injected ...). With the upstream
	// XCom this fixture sets, the captured stream is:
	//   ["▸ task started", "running: ...", "injected ...", "line one", "line two", "✓ task succeeded ..."]
	if len(sink.lines) != 6 {
		t.Fatalf("log lines = %v (len=%d), want 6 (framing+boot+2lifecycle+2 lines+ framing)", sink.lines, len(sink.lines))
	}
	if !strings.Contains(sink.lines[0], "task started") {
		t.Errorf("line[0] must be the start framing, got %q", sink.lines[0])
	}
	if !strings.HasPrefix(sink.lines[1], "running:") {
		t.Errorf("line[1] must be the boot argv line, got %q", sink.lines[1])
	}
	if !strings.HasPrefix(sink.lines[2], "injected ") {
		t.Errorf("line[2] must be the injected env line, got %q", sink.lines[2])
	}
	if sink.lines[3] != "line one" || sink.lines[4] != "line two" {
		t.Errorf("lines[3..4] should be the user stdout 'line one' / 'line two', got %q / %q", sink.lines[3], sink.lines[4])
	}
	if !strings.Contains(sink.lines[5], "succeeded") {
		t.Errorf("last line must be the success framing, got %q", sink.lines[5])
	}
	if !sink.closed {
		t.Error("log sink should be closed after the command exits")
	}
}

func TestRunnerReportsFailureOnNonZeroExit(t *testing.T) {
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "bash", Entrypoint: "exit 1"}}
	cmd := &fakeCmd{exitCode: 1}
	r := newRunner(client, cmd, &recordingSink{})

	err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected an error when the task exits non-zero")
	}
	last := client.states[len(client.states)-1]
	if last != agentv1.TaskState_TASK_STATE_FAILED {
		t.Errorf("final state = %v, want failed", last)
	}
}

func TestRunnerPushesReturnValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "return_value.json")
	if err := os.WriteFile(path, []byte(`{"result":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:f"}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	r.ReturnPath = path

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(client.pushed) != 1 {
		t.Fatalf("expected one PushXCom, got %d", len(client.pushed))
	}
	if string(client.pushed[0].GetValue()) != `{"result":42}` {
		t.Errorf("pushed value = %s", client.pushed[0].GetValue())
	}
}

func TestRunnerToleratesUnimplementedPush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "return_value.json")
	if err := os.WriteFile(path, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{
		spec:    &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:f"},
		pushErr: status.Error(codes.Unimplemented, "xcom not implemented yet"),
	}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	r.ReturnPath = path

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("an Unimplemented push must not fail the task: %v", err)
	}
	if last := client.states[len(client.states)-1]; last != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("final state = %v, want success", last)
	}
}

func TestRunnerFailsOnRealPushError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "return_value.json")
	if err := os.WriteFile(path, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{
		spec:    &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:f"},
		pushErr: status.Error(codes.Internal, "boom"),
	}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	r.ReturnPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Error("a non-Unimplemented push error should fail the task")
	}
}

func TestRunnerHeartbeatCancelsOnTerminate(t *testing.T) {
	client := &fakeClient{
		spec:               &agentv1.TaskSpec{Operator: "bash", Entrypoint: "sleep 1000"},
		heartbeatTerminate: true,
	}
	cmd := &fakeCmd{blockUntilCancel: true}
	r := newRunner(client, cmd, &recordingSink{})
	r.HeartbeatInterval = 5 * time.Millisecond

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a terminated task should fail")
	}
	if last := client.states[len(client.states)-1]; last != agentv1.TaskState_TASK_STATE_FAILED {
		t.Errorf("final state = %v, want failed after termination", last)
	}
}

// TestRunnerEnforcesExecutionTimeout covers issue #194: the pod-path agent must
// stop a task that exceeds its declared execution_timeout_seconds, not rely on
// the scheduler's 90 s heartbeat reaper (which leaves the wedged user code
// running until the agent itself notices). When the timeout fires, the failure
// is reported with a clear "execution_timeout" message so the operator can
// distinguish it from a crash or an exit-code failure.
func TestRunnerEnforcesExecutionTimeout(t *testing.T) {
	client := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator:                "bash",
			Entrypoint:              "sleep 1000",
			ExecutionTimeoutSeconds: 1, // proto carries seconds; the implementation uses time.Duration
		},
	}
	cmd := &fakeCmd{blockUntilCancel: true}
	r := newRunner(client, cmd, &recordingSink{})

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a task that exceeds its execution_timeout_seconds must fail")
	}
	if last := client.states[len(client.states)-1]; last != agentv1.TaskState_TASK_STATE_FAILED {
		t.Errorf("final state = %v, want failed after timeout", last)
	}
	// The terminal report must name the failure as a timeout (not "task exited
	// non-zero"), so the user can tell a hang from a crash.
	if n := len(client.reports); n > 0 {
		got := client.reports[n-1].GetErrorMessage()
		if !strings.Contains(got, "execution_timeout") {
			t.Errorf("error message should name execution_timeout, got %q", got)
		}
	}
}

// TestRunnerHonorsZeroTimeout keeps the existing semantics for tasks with no
// declared timeout (0): no time bound, the task runs until it finishes or the
// agent process itself is canceled.
func TestRunnerHonorsZeroTimeout(t *testing.T) {
	client := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator:                "python",
			Entrypoint:              "dag:fast",
			ExecutionTimeoutSeconds: 0, // unset → no timeout applies
		},
	}
	cmd := &fakeCmd{exitCode: 0}
	r := newRunner(client, cmd, &recordingSink{})

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("zero execution_timeout_seconds must not interfere with a fast task: %v", err)
	}
	if last := client.states[len(client.states)-1]; last != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("final state = %v, want success", last)
	}
}

func TestRunnerSkipsAbsentXComInput(t *testing.T) {
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator: "python", Entrypoint: "dag:f",
		XcomInputMapping: map[string]string{"maybe": "upstream"}, // upstream pushed nothing
	}}
	cmd := &fakeCmd{}
	r := newRunner(client, cmd, &recordingSink{})

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("an absent declared xcom input must not fail the task: %v", err)
	}
	if strings.Contains(strings.Join(cmd.env, "\n"), "LEOFLOW_XCOM_MAYBE") {
		t.Errorf("absent xcom input should be skipped (None), not set: %v", cmd.env)
	}
}

func TestRunnerRejectsHTTPOperator(t *testing.T) {
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "http_api"}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	if err := r.Run(context.Background()); err == nil {
		t.Error("agent must refuse to run http_api tasks")
	}
}

func TestRunnerPropagatesGetSpecError(t *testing.T) {
	client := &fakeClient{getSpecErr: errors.New("boom")}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	if err := r.Run(context.Background()); err == nil {
		t.Error("GetTaskSpec failure should abort the run")
	}
}
