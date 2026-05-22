package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc"
)

// fakeClient is a test double for the generated AgentServiceClient.
type fakeClient struct {
	spec        *agentv1.TaskSpec
	xcom        map[string]*agentv1.FetchXComResponse
	states      []agentv1.TaskState
	pushed      []*agentv1.PushXComRequest
	registered  bool
	terminateAt agentv1.TaskState // state for which ReportState returns should_terminate
	getSpecErr  error
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
	return nil, errors.New("no xcom for " + in.GetUpstreamTaskId())
}

func (f *fakeClient) PushXCom(_ context.Context, in *agentv1.PushXComRequest, _ ...grpc.CallOption) (*agentv1.PushXComResponse, error) {
	f.pushed = append(f.pushed, in)
	return &agentv1.PushXComResponse{Accepted: true}, nil
}

func (f *fakeClient) StreamLogs(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[agentv1.LogLine, agentv1.LogAck], error) {
	return nil, errors.New("not used in these tests")
}

func (f *fakeClient) ReportState(_ context.Context, in *agentv1.ReportStateRequest, _ ...grpc.CallOption) (*agentv1.ReportStateResponse, error) {
	f.states = append(f.states, in.GetState())
	return &agentv1.ReportStateResponse{Acknowledged: true, ShouldTerminate: in.GetState() == f.terminateAt}, nil
}

func (f *fakeClient) Heartbeat(context.Context, *agentv1.HeartbeatRequest, ...grpc.CallOption) (*agentv1.HeartbeatResponse, error) {
	return &agentv1.HeartbeatResponse{}, nil
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
	argv     []string
	env      []string
	stdout   string
	exitCode int
	err      error
}

func (c *fakeCmd) Run(_ context.Context, argv, env []string, stdout, _ io.Writer) (int, error) {
	c.argv, c.env = argv, env
	if c.stdout != "" {
		_, _ = io.WriteString(stdout, c.stdout)
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
	if len(sink.lines) != 2 || sink.lines[0] != "line one" {
		t.Errorf("log lines = %v, want two captured lines", sink.lines)
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
