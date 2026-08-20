package agentrpc

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─── test doubles ───────────────────────────────────────────────────────────

// fakeAwaitStream is an in-memory bidi AwaitAssignment server stream. The test
// pushes WorkerMessages (and terminal errors) onto recv; assignments the handler
// Sends land on sent.
type fakeAwaitStream struct {
	grpc.ServerStream
	ctx  context.Context
	recv chan recvItem
	sent chan *agentv1.WorkAssignment
}

type recvItem struct {
	m   *agentv1.WorkerMessage
	err error
}

func newFakeAwaitStream(ctx context.Context) *fakeAwaitStream {
	return &fakeAwaitStream{
		ctx:  ctx,
		recv: make(chan recvItem, 16),
		sent: make(chan *agentv1.WorkAssignment, 16),
	}
}

func (s *fakeAwaitStream) Context() context.Context { return s.ctx }

func (s *fakeAwaitStream) Recv() (*agentv1.WorkerMessage, error) {
	it := <-s.recv
	return it.m, it.err
}

func (s *fakeAwaitStream) Send(a *agentv1.WorkAssignment) error {
	s.sent <- a
	return nil
}

func (s *fakeAwaitStream) pushMsg(m *agentv1.WorkerMessage) { s.recv <- recvItem{m: m} }
func (s *fakeAwaitStream) pushErr(err error)                { s.recv <- recvItem{err: err} }

func regMsg(dagVersion string) *agentv1.WorkerMessage {
	return &agentv1.WorkerMessage{Msg: &agentv1.WorkerMessage_Register{
		Register: &agentv1.WorkerRegister{DagVersionId: dagVersion},
	}}
}

func ackMsg(assignmentID string, started bool) *agentv1.WorkerMessage {
	return &agentv1.WorkerMessage{Msg: &agentv1.WorkerMessage_Ack{
		Ack: &agentv1.AssignmentAck{AssignmentId: assignmentID, Started: started},
	}}
}

// newWarmServer builds a Server with warm pools ENABLED (a registry wired) plus
// the JWT authenticator whose token ctxWithToken mints (identity "ti-1").
func newWarmServer(t *testing.T, onReclaim func(ReclaimEvent)) (*Server, *auth.JWTAuthenticator, *workerRegistry) {
	t.Helper()
	srv, a := newServer(&fakeStore{})
	reg := newWorkerRegistry(onReclaim)
	srv.SetWarmPools(reg)
	return srv, a, reg
}

// awaitEventually polls cond until true or the deadline, failing the test otherwise.
func awaitEventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

// ─── (a) inert when warm pools off ──────────────────────────────────────────

func TestAwaitAssignmentInertWhenWarmPoolsOff(t *testing.T) {
	srv, a := newServer(&fakeStore{}) // no SetWarmPools => off
	stream := newFakeAwaitStream(ctxWithToken(t, a))
	err := srv.AwaitAssignment(stream)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("warm pools off: got %v, want FailedPrecondition", err)
	}
}

// ─── (b) non-register first message ─────────────────────────────────────────

func TestAwaitAssignmentRequiresRegisterFirst(t *testing.T) {
	srv, a, _ := newWarmServer(t, nil)
	stream := newFakeAwaitStream(ctxWithToken(t, a))
	stream.pushMsg(ackMsg("as-1", true)) // first message is an ack, not a register
	err := srv.AwaitAssignment(stream)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("non-register first message: got %v, want FailedPrecondition", err)
	}
}

// ─── (c) register => present in registry ────────────────────────────────────

func TestAwaitAssignmentRegistersWorker(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	stream := newFakeAwaitStream(ctxWithToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	go func() { _ = srv.AwaitAssignment(stream) }()

	awaitEventually(t, func() bool { return reg.registered("ti-1") })
	if got := reg.dagVersionOf("ti-1"); got != "dagver-1" {
		t.Fatalf("registered dag version = %q, want dagver-1", got)
	}
	stream.pushErr(io.EOF) // clean shutdown
}

// ─── (d) Assign pushes a WorkAssignment the stream receives ─────────────────

func TestAwaitAssignmentDeliversAssignment(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	stream := newFakeAwaitStream(ctxWithToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	go func() { _ = srv.AwaitAssignment(stream) }()
	awaitEventually(t, func() bool { return reg.registered("ti-1") })

	if reg.Assign("other-version", &agentv1.WorkAssignment{AssignmentId: "x"}) {
		t.Fatal("Assign to a dag version with no free worker should return false")
	}
	if !reg.Assign("dagver-1", &agentv1.WorkAssignment{AssignmentId: "as-1", TaskId: "extract", LeaseSeconds: 30}) {
		t.Fatal("Assign to a free worker's dag version should return true")
	}
	select {
	case got := <-stream.sent:
		if got.GetAssignmentId() != "as-1" || got.GetTaskId() != "extract" {
			t.Fatalf("delivered assignment = %+v, want as-1/extract", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no assignment delivered to the stream")
	}
	stream.pushErr(io.EOF)
}

// ─── (e) ack started=true within lease => busy, no reclaim ──────────────────

func TestAckStartedWithinLeaseMarksBusyNoReclaim(t *testing.T) {
	reclaims := make(chan ReclaimEvent, 4)
	reg := newWorkerRegistry(func(ev ReclaimEvent) { reclaims <- ev })
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return time.Hour } // long lease

	send := make(chan *agentv1.WorkAssignment, 1)
	reg.Register("w1", "v1", send)
	if !reg.Assign("v1", &agentv1.WorkAssignment{AssignmentId: "as-1"}) {
		t.Fatal("Assign should succeed")
	}
	<-send // drain the delivered assignment
	reg.Ack("as-1", true)

	if !reg.busy("w1") {
		t.Fatal("worker should be marked busy after ack started=true")
	}
	select {
	case ev := <-reclaims:
		t.Fatalf("unexpected reclaim after ack started=true: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// ─── (f) lease expiry with no ack => reclaim ────────────────────────────────

func TestLeaseExpiryReclaims(t *testing.T) {
	reclaims := make(chan ReclaimEvent, 4)
	reg := newWorkerRegistry(func(ev ReclaimEvent) { reclaims <- ev })
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return 5 * time.Millisecond }

	send := make(chan *agentv1.WorkAssignment, 1)
	reg.Register("w1", "v1", send)
	reg.Assign("v1", &agentv1.WorkAssignment{AssignmentId: "as-1", DagVersionId: "v1"})
	<-send

	select {
	case ev := <-reclaims:
		if ev.AssignmentID != "as-1" || ev.Reason != ReclaimLeaseExpired {
			t.Fatalf("reclaim = %+v, want as-1/ReclaimLeaseExpired", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a reclaim event on lease expiry")
	}
}

// ─── (g) ack started=false => reclaim ───────────────────────────────────────

func TestAckRefusedReclaims(t *testing.T) {
	reclaims := make(chan ReclaimEvent, 4)
	reg := newWorkerRegistry(func(ev ReclaimEvent) { reclaims <- ev })
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return time.Hour }

	send := make(chan *agentv1.WorkAssignment, 1)
	reg.Register("w1", "v1", send)
	reg.Assign("v1", &agentv1.WorkAssignment{AssignmentId: "as-1", DagVersionId: "v1"})
	<-send
	reg.Ack("as-1", false)

	select {
	case ev := <-reclaims:
		if ev.AssignmentID != "as-1" || ev.Reason != ReclaimRefused {
			t.Fatalf("reclaim = %+v, want as-1/ReclaimRefused", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a reclaim event on ack started=false")
	}
	if reg.busy("w1") {
		t.Fatal("a worker that refused an assignment must not be marked busy")
	}
}

// ─── (h) stream close => deregistered ───────────────────────────────────────

func TestAwaitAssignmentDeregistersOnStreamClose(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	stream := newFakeAwaitStream(ctxWithToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	done := make(chan error, 1)
	go func() { done <- srv.AwaitAssignment(stream) }()

	awaitEventually(t, func() bool { return reg.registered("ti-1") })
	stream.pushErr(io.EOF)

	if err := <-done; err != nil {
		t.Fatalf("clean stream close should return nil, got %v", err)
	}
	awaitEventually(t, func() bool { return !reg.registered("ti-1") })
}

// ─── (i) reconnect same identity => single entry ────────────────────────────

func TestAwaitAssignmentReconnectSameIdentitySingleEntry(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)

	s1 := newFakeAwaitStream(ctxWithToken(t, a))
	s1.pushMsg(regMsg("v1"))
	go func() { _ = srv.AwaitAssignment(s1) }()
	awaitEventually(t, func() bool { return reg.dagVersionOf("ti-1") == "v1" })

	// A reconnect with the SAME authenticated identity but a new registration.
	s2 := newFakeAwaitStream(ctxWithToken(t, a))
	s2.pushMsg(regMsg("v2"))
	go func() { _ = srv.AwaitAssignment(s2) }()
	awaitEventually(t, func() bool { return reg.dagVersionOf("ti-1") == "v2" })

	if reg.size() != 1 {
		t.Fatalf("reconnect same identity: registry size = %d, want 1", reg.size())
	}
}
