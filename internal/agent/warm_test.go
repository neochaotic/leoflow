package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

// TestWarmWorkerRegisterCarriesPodName locks the durable-binding key on the wire
// (ADR 0058 N1d-a1): the worker's own pod name (from LEOFLOW_POD_NAME, threaded
// in as WarmRunner.PodName) rides in WorkerRegister.pod_name so the control plane
// can bind a started attempt to it. No assignments follow, so Run registers and
// exits on the stream's EOF.
func TestWarmWorkerRegisterCarriesPodName(t *testing.T) {
	stream := &fakeAssignmentStream{ctx: context.Background()} // empty => immediate io.EOF
	client := &warmFake{fakeClient: &fakeClient{}, stream: stream, tokens: NewTokenSource("bootstrap")}

	w := &WarmRunner{
		StreamClient:  client,
		WorkClient:    client,
		AttemptTokens: NewTokenSource("bootstrap"),
		Cmd:           &scratchProbeCmd{scratchDir: filepath.Join(t.TempDir(), "scratch")},
		Hostname:      "warm-pod-1",
		Version:       "test",
		PodName:       "leoflow-warm-dv-abc-x7k2",
		ScratchDir:    filepath.Join(t.TempDir(), "scratch"),
	}

	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v", err)
	}
	if len(stream.sent) == 0 {
		t.Fatal("worker sent no messages; expected a WorkerRegister")
	}
	reg := stream.sent[0].GetRegister()
	if reg == nil {
		t.Fatalf("first message must be a WorkerRegister, got %+v", stream.sent[0])
	}
	if reg.GetPodName() != "leoflow-warm-dv-abc-x7k2" {
		t.Errorf("WorkerRegister.pod_name = %q, want leoflow-warm-dv-abc-x7k2", reg.GetPodName())
	}
}

// warmSpecs returns n identical minimal specs (bash true, no execution_timeout) so
// a warmFake can hand one out per served attempt without index-out-of-range.
func warmSpecs(n int) []*agentv1.TaskSpec {
	out := make([]*agentv1.TaskSpec, n)
	for i := range out {
		out[i] = &agentv1.TaskSpec{Operator: "bash", Entrypoint: "true"}
	}
	return out
}

// TestWarmWorkerDrainsAtMaxAttempts is D9/D10 (ADR 0058): a warm worker serves at
// most MaxAttempts attempts, then DRAINS — Run returns nil (the process exits and
// the reconciler replaces the pod). The stream offers more work than the cap, so
// the only reason exactly two attempts run is the cap, and the drain is graceful
// (checked after SlotFree, never mid-attempt).
func TestWarmWorkerDrainsAtMaxAttempts(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "scratch")
	stream := &fakeAssignmentStream{
		ctx: context.Background(),
		assignments: []*agentv1.WorkAssignment{
			{AssignmentId: "asg-1", AttemptToken: "tok-1"},
			{AssignmentId: "asg-2", AttemptToken: "tok-2"},
			{AssignmentId: "asg-3", AttemptToken: "tok-3"},
		},
	}
	tokens := NewTokenSource("bootstrap")
	client := &warmFake{fakeClient: &fakeClient{}, stream: stream, tokens: tokens, specs: warmSpecs(3)}
	cmd := &scratchProbeCmd{scratchDir: scratch}

	w := &WarmRunner{
		StreamClient: client, WorkClient: client, AttemptTokens: tokens,
		Cmd: cmd, Hostname: "warm-pod-1", Version: "test", ScratchDir: scratch,
		MaxAttempts: 2,
	}
	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v", err)
	}
	if cmd.runs != 2 {
		t.Fatalf("attempts run = %d, want exactly 2 (drained at MaxAttempts)", cmd.runs)
	}
	// The worker Recv'd only two assignments — it drained rather than pulling asg-3.
	if stream.idx != 2 {
		t.Errorf("assignments received = %d, want 2 (drain must stop awaiting new work)", stream.idx)
	}
}

// TestWarmWorkerDrainsAtMaxLifetime is D9's wall-clock cap: with a 1ns lifetime the
// after-SlotFree check trips on the very first attempt, so the worker serves one and
// drains. Deterministic — any elapsed time since workerStart exceeds 1ns.
func TestWarmWorkerDrainsAtMaxLifetime(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "scratch")
	stream := &fakeAssignmentStream{
		ctx: context.Background(),
		assignments: []*agentv1.WorkAssignment{
			{AssignmentId: "asg-1", AttemptToken: "tok-1"},
			{AssignmentId: "asg-2", AttemptToken: "tok-2"},
		},
	}
	tokens := NewTokenSource("bootstrap")
	client := &warmFake{fakeClient: &fakeClient{}, stream: stream, tokens: tokens, specs: warmSpecs(2)}
	cmd := &scratchProbeCmd{scratchDir: scratch}

	w := &WarmRunner{
		StreamClient: client, WorkClient: client, AttemptTokens: tokens,
		Cmd: cmd, Hostname: "warm-pod-1", Version: "test", ScratchDir: scratch,
		MaxLifetime: time.Nanosecond,
	}
	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v", err)
	}
	if cmd.runs != 1 {
		t.Fatalf("attempts run = %d, want exactly 1 (drained at MaxLifetime)", cmd.runs)
	}
	if stream.idx != 1 {
		t.Errorf("assignments received = %d, want 1 (lifetime drain stops awaiting new work)", stream.idx)
	}
}

// idleStream serves one assignment, then BLOCKS in Recv until its context is
// canceled — modeling a real gRPC stream whose Recv only returns when the stream
// closes. The idle-TTL path in Run must recycle the worker BEFORE this returns; the
// blocked Recv goroutine then unblocks on the parent cancel and closes recvExited,
// letting the test prove no goroutine is leaked.
type idleStream struct {
	ctx        context.Context
	first      *agentv1.WorkAssignment
	served     bool
	sent       []*agentv1.WorkerMessage
	recvExited chan struct{}
}

func (s *idleStream) Send(m *agentv1.WorkerMessage) error { s.sent = append(s.sent, m); return nil }

func (s *idleStream) Recv() (*agentv1.WorkAssignment, error) {
	if !s.served {
		s.served = true
		return s.first, nil
	}
	<-s.ctx.Done()
	close(s.recvExited)
	return nil, status.Error(codes.Canceled, "stream closed")
}

func (s *idleStream) Header() (metadata.MD, error) { return metadata.MD{}, nil }
func (s *idleStream) Trailer() metadata.MD         { return nil }
func (s *idleStream) CloseSend() error             { return nil }
func (s *idleStream) Context() context.Context     { return s.ctx }
func (s *idleStream) SendMsg(any) error            { return nil }
func (s *idleStream) RecvMsg(any) error            { return nil }

// idleFake overrides AwaitAssignment to hand back the blocking idleStream.
type idleFake struct {
	*fakeClient
	stream *idleStream
}

func (c *idleFake) AwaitAssignment(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[agentv1.WorkerMessage, agentv1.WorkAssignment], error) {
	return c.stream, nil
}

// TestWarmWorkerIdleRecycle is D6: while awaiting the next assignment, a worker that
// sees no work within IdleTTL recycles itself (Run returns nil) for freshness /
// scale-down. The recv goroutine must not leak — once the stream closes on teardown
// its blocked Recv returns and the goroutine exits.
func TestWarmWorkerIdleRecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scratch := filepath.Join(t.TempDir(), "scratch")
	stream := &idleStream{
		ctx:        ctx,
		first:      &agentv1.WorkAssignment{AssignmentId: "asg-1", AttemptToken: "tok-1"},
		recvExited: make(chan struct{}),
	}
	tokens := NewTokenSource("bootstrap")
	client := &idleFake{fakeClient: &fakeClient{spec: &agentv1.TaskSpec{Operator: "bash", Entrypoint: "true"}}}
	client.stream = stream

	w := &WarmRunner{
		StreamClient: client, WorkClient: client, AttemptTokens: tokens,
		Cmd: &scratchProbeCmd{scratchDir: scratch}, Hostname: "warm-pod-1", Version: "test",
		ScratchDir: scratch,
		IdleTTL:    20 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, "dagver-1") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WarmRunner.Run returned %v, want nil (idle recycle)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not idle-recycle within 2s")
	}

	// No goroutine leak: after Run returns, tear down the stream (parent cancel) and
	// prove the blocked Recv goroutine unblocks and exits.
	cancel()
	select {
	case <-stream.recvExited:
	case <-time.After(2 * time.Second):
		t.Fatal("recv goroutine leaked: blocked Recv never returned after stream close")
	}
}

// wedgeThenOKCmd wedges its first invocation until the ctx is canceled (recording
// the cancel cause), then succeeds on every later invocation — modeling a task with
// no execution_timeout that hangs, followed by a well-behaved one.
type wedgeThenOKCmd struct {
	runs         int
	wedgedCtxErr error
}

func (c *wedgeThenOKCmd) Run(ctx context.Context, _, _ []string, _, _ io.Writer) (int, error) {
	c.runs++
	if c.runs == 1 {
		<-ctx.Done()
		c.wedgedCtxErr = ctx.Err()
		return 137, ctx.Err()
	}
	return 0, nil
}

// TestWarmWorkerWatchdogKillsWedgedAttempt is H3: an always-on per-attempt watchdog,
// INDEPENDENT of the task's execution_timeout (0 here), hard-bounds a wedged attempt.
// The first attempt hangs and is killed at the watchdog — reported FAILED, SlotFree
// still sent — and the worker keeps serving, running the second attempt to SUCCESS.
func TestWarmWorkerWatchdogKillsWedgedAttempt(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "scratch")
	stream := &fakeAssignmentStream{
		ctx: context.Background(),
		assignments: []*agentv1.WorkAssignment{
			{AssignmentId: "asg-1", AttemptToken: "tok-1"},
			{AssignmentId: "asg-2", AttemptToken: "tok-2"},
		},
	}
	tokens := NewTokenSource("bootstrap")
	// execution_timeout=0 on both specs: the ONLY ceiling is the watchdog.
	client := &warmFake{fakeClient: &fakeClient{}, stream: stream, tokens: tokens, specs: warmSpecs(2)}
	cmd := &wedgeThenOKCmd{}

	w := &WarmRunner{
		StreamClient: client, WorkClient: client, AttemptTokens: tokens,
		Cmd: cmd, Hostname: "warm-pod-1", Version: "test", ScratchDir: scratch,
		AttemptWatchdog: 20 * time.Millisecond,
	}
	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v (a wedged attempt must not be loop-fatal)", err)
	}

	if cmd.runs != 2 {
		t.Fatalf("attempts run = %d, want 2 (worker keeps serving after the watchdog kill)", cmd.runs)
	}
	// The wedge was cut by the watchdog deadline, not by execution_timeout (which was
	// 0) — proving the watchdog is an independent ceiling.
	if !errors.Is(cmd.wedgedCtxErr, context.DeadlineExceeded) {
		t.Errorf("wedged attempt ctx err = %v, want context.DeadlineExceeded (watchdog)", cmd.wedgedCtxErr)
	}
	// Attempt 1 RUNNING→FAILED (killed), attempt 2 RUNNING→SUCCESS.
	wantStates := []agentv1.TaskState{
		agentv1.TaskState_TASK_STATE_RUNNING, agentv1.TaskState_TASK_STATE_FAILED,
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
	// SlotFree still fires for BOTH attempts (register + 2×[ack, slotfree] = 5).
	slotFrees := 0
	for _, m := range stream.sent {
		if m.GetSlotFree() != nil {
			slotFrees++
		}
	}
	if slotFrees != 2 {
		t.Errorf("SlotFree count = %d, want 2 (slot must free even after a watchdog kill)", slotFrees)
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
