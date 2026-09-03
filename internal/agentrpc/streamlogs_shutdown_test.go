package agentrpc

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/agent"
	"github.com/neochaotic/leoflow/internal/logs"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestWriteLinesReturnsOnShutdown pins the shutdown half of the eviction bug
// class: StreamLogs is a bidi stream held open for the WHOLE task, and its
// receive loop blocked on Recv with no way to be told the process is stopping.
// GracefulStop then waited on every such stream until the kubelet's SIGKILL, so
// no deferred flush ever ran. The loop must return promptly once the shutdown
// signal fires, even though the agent never closes its side.
func TestWriteLinesReturnsOnShutdown(t *testing.T) {
	unblock := make(chan struct{})
	recv := func() (*agentv1.LogLine, error) {
		<-unblock // the agent keeps the stream open: Recv never returns on its own
		return nil, io.EOF
	}
	shutdown := make(chan struct{})
	w := &fakeLogWriter{}
	errCh := make(chan error, 1)
	go func() { errCh <- writeLines(shutdown, w, recv, func(string) {}) }()

	select {
	case err := <-errCh:
		t.Fatalf("writeLines returned %v before shutdown with a blocked Recv", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(shutdown)
	select {
	case err := <-errCh:
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("writeLines on shutdown = %v, want codes.Unavailable so the agent treats it as a transient stream end", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("writeLines did not return within 2s of the shutdown signal")
	}
	close(unblock) // let the receiver goroutine exit; it must not block or panic
}

// TestWriteLinesDeliversLinesBeforeShutdown: lines that arrive before the signal
// are written; the shutdown only ends the wait for more.
func TestWriteLinesDeliversLinesBeforeShutdown(t *testing.T) {
	lines := make(chan *agentv1.LogLine, 2)
	lines <- &agentv1.LogLine{Message: "one"}
	lines <- &agentv1.LogLine{Message: "two"}
	shutdown := make(chan struct{})
	recv := func() (*agentv1.LogLine, error) {
		select {
		case l := <-lines:
			return l, nil
		case <-shutdown:
			<-shutdown // hold until the test releases the receiver
			return nil, io.EOF
		}
	}
	w := &fakeLogWriter{}
	published := make(chan struct{}, 2) // publish runs after each WriteEvent, on the loop goroutine
	errCh := make(chan error, 1)
	go func() { errCh <- writeLines(shutdown, w, recv, func(string) { published <- struct{}{} }) }()
	for i := 0; i < 2; i++ {
		select {
		case <-published:
		case <-time.After(2 * time.Second):
			t.Fatalf("line %d was not written within 2s", i+1)
		}
	}
	close(shutdown)
	if err := <-errCh; status.Code(err) != codes.Unavailable {
		t.Fatalf("writeLines = %v, want Unavailable", err)
	}
	// Reading w.lines is ordered after the loop's writes by the errCh receive.
	if len(w.lines) != 2 || w.lines[0] != "one" || w.lines[1] != "two" {
		t.Errorf("lines written before shutdown = %v, want [one two]", w.lines)
	}
}

// blockingStreamLogsServer is a bidi server stream whose agent never sends EOF:
// Recv blocks until the stream context ends, exactly like a real stream held
// open by a long-running task.
type blockingStreamLogsServer struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *blockingStreamLogsServer) Context() context.Context   { return s.ctx }
func (s *blockingStreamLogsServer) Send(*agentv1.LogAck) error { return nil }
func (s *blockingStreamLogsServer) Recv() (*agentv1.LogLine, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

// closeTrackingSink records whether the writer StreamLogs opened was Closed —
// the flush that a SIGKILL skips and that the shutdown path must now reach.
type closeTrackingSink struct {
	mu     sync.Mutex
	closed bool
}

func (c *closeTrackingSink) Open(logs.Ref) (logs.LogWriter, error) { return c, nil }
func (c *closeTrackingSink) WriteEvent(logs.Event) error           { return nil }
func (c *closeTrackingSink) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *closeTrackingSink) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// TestStreamLogsClosesWriterOnShutdown: with a shutdown context wired, a
// StreamLogs whose agent is still streaming returns when that context ends and
// runs its deferred writer Close (the sink's final flush) before returning.
func TestStreamLogsClosesWriterOnShutdown(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	sink := &closeTrackingSink{}
	srv.SetLogSink(sink)
	shutdownCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	srv.SetShutdown(shutdownCtx)

	streamCtx, cancelStream := context.WithCancel(ctxWithToken(t, a))
	defer cancelStream()
	stream := &blockingStreamLogsServer{ctx: streamCtx}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.StreamLogs(stream) }()

	select {
	case err := <-errCh:
		t.Fatalf("StreamLogs returned %v while the agent was still streaming", err)
	case <-time.After(50 * time.Millisecond):
	}
	shutdown()
	select {
	case err := <-errCh:
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("StreamLogs on shutdown = %v, want Unavailable", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StreamLogs did not return within 2s of shutdown")
	}
	if !sink.isClosed() {
		t.Fatal("the log writer was not Closed on shutdown: the attempt's final flush was skipped")
	}
}

// TestStreamLogsWithoutShutdownContextBehavesAsBefore: a server that never wired
// a shutdown context (tests, embedders) keeps the pre-existing semantics — the
// stream ends only when the agent half-closes.
func TestStreamLogsWithoutShutdownContextBehavesAsBefore(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	sink := &fakeLogSink{}
	srv.SetLogSink(sink)
	stream := &fakeStreamLogsServer{ctx: ctxWithToken(t, a), msgs: []*agentv1.LogLine{{Message: "hello"}}}
	if err := srv.StreamLogs(stream); err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	if len(sink.lines) != 1 || sink.lines[0] != "hello" {
		t.Errorf("sink lines = %v, want [hello]", sink.lines)
	}
}

// TestStreamLogsShutdownOverRealTransport drives the production agent client
// against a real gRPC server and then fires the control plane's shutdown while
// the task is still streaming. It pins three things a fake stream cannot: the
// server-side writer is Closed (flushed) with every line received so far; the
// agent's log sink survives the server ending the stream (Send errors are
// best-effort, Close returns promptly — the TASK is unaffected); and
// GracefulStop, which used to hang on this exact stream until SIGKILL, now
// completes within a bound because the stream has ended.
func TestStreamLogsShutdownOverRealTransport(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{Operator: "python"}}
	srv, authn := newServer(store)
	sink := &captureSink{}
	srv.SetLogSink(sink)
	shutdownCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	srv.SetShutdown(shutdownCtx)

	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gsrv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(gsrv, srv)
	go func() { _ = gsrv.Serve(lis) }()
	defer gsrv.Stop()

	token, err := authn.IssueAgentToken(testIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	client, conn, _, err := agent.Dial(lis.Addr().String(), token, true, "")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientSink, err := agent.OpenLogSink(ctx, client)
	if err != nil {
		t.Fatalf("OpenLogSink: %v", err)
	}
	const before = 5
	for i := 0; i < before; i++ {
		if serr := clientSink.Send(&agentv1.LogLine{Message: "pre", LineNumber: int64(i + 1)}); serr != nil {
			t.Fatalf("Send(%d): %v", i, serr)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := sink.snapshot(); len(got) == before {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, _ := sink.snapshot(); len(got) != before {
		t.Fatalf("server received %d lines before shutdown, want %d", len(got), before)
	}

	// SIGTERM: the control plane's shutdown context ends while the task streams.
	shutdown()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, closed := sink.snapshot(); closed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, closed := sink.snapshot(); !closed || len(got) != before {
		t.Fatalf("after shutdown: writer closed=%v lines=%d, want closed with the %d lines flushed", closed, len(got), before)
	}

	// The agent keeps running its task and keeps calling Send; the stream is gone,
	// so Send fails — best-effort, never a panic — and Close returns quickly.
	var sendErr error
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && sendErr == nil {
		sendErr = clientSink.Send(&agentv1.LogLine{Message: "post"})
		time.Sleep(10 * time.Millisecond)
	}
	if sendErr == nil {
		t.Fatal("agent Send kept succeeding after the server ended the stream")
	}
	closeStart := time.Now()
	if cerr := clientSink.Close(); cerr != nil && !errors.Is(cerr, io.EOF) {
		t.Logf("agent sink Close after server shutdown = %v (tolerated: best-effort)", cerr)
	}
	if took := time.Since(closeStart); took > 4*time.Second {
		t.Fatalf("agent sink Close took %s after the server ended the stream; must not stall the task", took)
	}

	// The whole point: with the stream ended, GracefulStop returns instead of
	// waiting for the task to finish.
	done := make(chan struct{})
	go func() { gsrv.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("GracefulStop still hangs after the shutdown context ended the log streams")
	}
}
