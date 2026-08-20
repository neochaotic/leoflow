package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakeAssignmentStream is a hand-rolled grpc.BidiStreamingClient double for the
// AwaitAssignment stream. It yields the queued assignments in order (then io.EOF,
// so the warm loop exits cleanly) and records every WorkerMessage the worker sends
// up the stream, so a test can assert the ack / slot-free protocol.
type fakeAssignmentStream struct {
	ctx         context.Context
	assignments []*agentv1.WorkAssignment
	idx         int
	sent        []*agentv1.WorkerMessage
	recvErr     error // returned once assignments are exhausted; io.EOF when nil
}

func (s *fakeAssignmentStream) Send(m *agentv1.WorkerMessage) error {
	s.sent = append(s.sent, m)
	return nil
}

func (s *fakeAssignmentStream) Recv() (*agentv1.WorkAssignment, error) {
	if s.idx >= len(s.assignments) {
		if s.recvErr != nil {
			return nil, s.recvErr
		}
		return nil, io.EOF
	}
	a := s.assignments[s.idx]
	s.idx++
	return a, nil
}

func (s *fakeAssignmentStream) Header() (metadata.MD, error) { return metadata.MD{}, nil }
func (s *fakeAssignmentStream) Trailer() metadata.MD         { return nil }
func (s *fakeAssignmentStream) CloseSend() error             { return nil }
func (s *fakeAssignmentStream) Context() context.Context     { return s.ctx }
func (s *fakeAssignmentStream) SendMsg(any) error            { return nil }
func (s *fakeAssignmentStream) RecvMsg(any) error            { return nil }

// warmFake is the control-plane double for the warm-worker tests. It embeds the
// single-shot fakeClient (reusing its Register / ReportState / secrets stubs) and
// overrides AwaitAssignment (to hand back the scripted stream) and GetTaskSpec (to
// return a distinct spec per attempt and record which bearer token was live when
// the per-attempt RPC ran — proving the worker adopts each attempt_token).
type warmFake struct {
	*fakeClient
	stream      *fakeAssignmentStream
	tokens      *TokenSource
	specs       []*agentv1.TaskSpec
	specIdx     int
	tokenAtSpec []string
}

func (c *warmFake) AwaitAssignment(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[agentv1.WorkerMessage, agentv1.WorkAssignment], error) {
	return c.stream, nil
}

func (c *warmFake) GetTaskSpec(context.Context, *agentv1.GetTaskSpecRequest, ...grpc.CallOption) (*agentv1.TaskSpec, error) {
	if c.tokens != nil {
		c.tokenAtSpec = append(c.tokenAtSpec, c.tokens.Token())
	}
	s := c.specs[c.specIdx]
	c.specIdx++
	return s, nil
}

// scratchProbeCmd is a CommandRunner double that, on each invocation, records
// whether a marker file already exists in the agent scratch dir, then writes it.
// Between warm attempts the worker must scrub the scratch (D4 isolation); if it
// does, attempt 2 sees the marker ABSENT even though attempt 1 wrote it.
type scratchProbeCmd struct {
	scratchDir string
	runs       int
	sawMarker  []bool
}

func (c *scratchProbeCmd) Run(_ context.Context, _, _ []string, _, _ io.Writer) (int, error) {
	marker := filepath.Join(c.scratchDir, "attempt-marker")
	_, statErr := os.Stat(marker)
	c.sawMarker = append(c.sawMarker, statErr == nil)
	_ = os.WriteFile(marker, []byte("written by an earlier attempt"), 0o600)
	c.runs++
	return 0, nil
}

// TestWarmWorkerServesTwoAttempts is the warm-mode contract: register once, open
// the assignment stream, and serve each assignment in a fresh forked child —
// acking before running, reporting the outcome, scrubbing scratch, and signaling
// SlotFree after each. The D4 scrub is proven by the marker file being absent when
// the second attempt starts.
func TestWarmWorkerServesTwoAttempts(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "scratch")
	stream := &fakeAssignmentStream{
		ctx: context.Background(),
		assignments: []*agentv1.WorkAssignment{
			{AssignmentId: "asg-1", AttemptToken: "tok-1", DagRunId: "run-a", TaskId: "t-a", TryNumber: 1},
			{AssignmentId: "asg-2", AttemptToken: "tok-2", DagRunId: "run-b", TaskId: "t-b", TryNumber: 2},
		},
	}
	tokens := NewTokenSource("bootstrap")
	client := &warmFake{
		fakeClient: &fakeClient{},
		stream:     stream,
		tokens:     tokens,
		specs: []*agentv1.TaskSpec{
			{Operator: "bash", Entrypoint: "true", RunId: "run-a", TaskId: "t-a", TryNumber: 1},
			{Operator: "bash", Entrypoint: "true", RunId: "run-b", TaskId: "t-b", TryNumber: 2},
		},
	}
	cmd := &scratchProbeCmd{scratchDir: scratch}

	w := &WarmRunner{
		StreamClient:  client,
		WorkClient:    client,
		AttemptTokens: tokens,
		Cmd:           cmd,
		Hostname:      "warm-pod-1",
		Version:       "test",
		Env:           []string{"PATH=/usr/bin"},
		ScratchDir:    scratch,
	}

	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v", err)
	}

	// Registered exactly once with the bootstrap identity.
	if !client.registered {
		t.Error("warm worker must register on startup")
	}

	// Both attempts ran as child execs.
	if cmd.runs != 2 {
		t.Fatalf("child execs = %d, want 2 (one per assignment)", cmd.runs)
	}

	// D4 isolation: attempt 1 wrote a marker into scratch; attempt 2 must NOT see
	// it — the worker scrubbed the shared scratch between attempts.
	if len(cmd.sawMarker) != 2 || cmd.sawMarker[0] || cmd.sawMarker[1] {
		t.Errorf("scratch scrub failed: sawMarker=%v, want [false false]", cmd.sawMarker)
	}

	// Each attempt ran under its own attempt_token (adopted before GetTaskSpec).
	wantTokens := []string{"tok-1", "tok-2"}
	if len(client.tokenAtSpec) != 2 || client.tokenAtSpec[0] != wantTokens[0] || client.tokenAtSpec[1] != wantTokens[1] {
		t.Errorf("token live at GetTaskSpec = %v, want %v", client.tokenAtSpec, wantTokens)
	}

	// Both attempts reported RUNNING then SUCCESS.
	wantStates := []agentv1.TaskState{
		agentv1.TaskState_TASK_STATE_RUNNING, agentv1.TaskState_TASK_STATE_SUCCESS,
		agentv1.TaskState_TASK_STATE_RUNNING, agentv1.TaskState_TASK_STATE_SUCCESS,
	}
	if len(client.states) != len(wantStates) {
		t.Fatalf("reported states = %v, want %v", client.states, wantStates)
	}
	for i, s := range wantStates {
		if client.states[i] != s {
			t.Fatalf("reported states = %v, want %v", client.states, wantStates)
		}
	}

	// Up-stream protocol: WorkerRegister first, then Ack(started)+SlotFree per
	// assignment, in that order.
	if len(stream.sent) != 5 {
		t.Fatalf("worker sent %d messages, want 5 (register + 2×[ack,slotfree]); got %+v", len(stream.sent), stream.sent)
	}
	if reg := stream.sent[0].GetRegister(); reg == nil || reg.GetDagVersionId() != "dagver-1" {
		t.Errorf("first message must be WorkerRegister(dagver-1), got %+v", stream.sent[0])
	}
	assertAck := func(idx int, wantID string) {
		ack := stream.sent[idx].GetAck()
		if ack == nil {
			t.Fatalf("message %d must be an AssignmentAck, got %+v", idx, stream.sent[idx])
		}
		if !ack.GetStarted() {
			t.Errorf("ack %d must have started=true", idx)
		}
		if ack.GetAssignmentId() != wantID {
			t.Errorf("ack %d assignment_id = %q, want %q", idx, ack.GetAssignmentId(), wantID)
		}
	}
	assertAck(1, "asg-1")
	if stream.sent[2].GetSlotFree() == nil {
		t.Errorf("message 2 must be SlotFree, got %+v", stream.sent[2])
	}
	assertAck(3, "asg-2")
	if stream.sent[4].GetSlotFree() == nil {
		t.Errorf("message 4 must be SlotFree, got %+v", stream.sent[4])
	}
}

// TestRunnerRunIsRegisterThenOneAttempt locks the single-shot refactor: Run must
// still be exactly "register, then run one attempt" — one GetTaskSpec, one
// RUNNING→SUCCESS pair — with no warm-loop behavior leaking in.
func TestRunnerRunIsRegisterThenOneAttempt(t *testing.T) {
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "bash", Entrypoint: "true"}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !client.registered {
		t.Error("Run must register")
	}
	wantStates := []agentv1.TaskState{agentv1.TaskState_TASK_STATE_RUNNING, agentv1.TaskState_TASK_STATE_SUCCESS}
	if len(client.states) != 2 || client.states[0] != wantStates[0] || client.states[1] != wantStates[1] {
		t.Errorf("states = %v, want exactly one attempt (running then success)", client.states)
	}
}
