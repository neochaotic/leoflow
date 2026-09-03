package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// hangingStopper models a grpc.Server with an in-flight stream the client never
// ends: GracefulStop blocks until Stop force-closes the transports.
type hangingStopper struct {
	stopped chan struct{}
	stops   atomic.Int32
}

func newHangingStopper() *hangingStopper { return &hangingStopper{stopped: make(chan struct{})} }

func (h *hangingStopper) GracefulStop() { <-h.stopped }
func (h *hangingStopper) Stop() {
	if h.stops.Add(1) == 1 {
		close(h.stopped)
	}
}

// promptStopper models a server with no in-flight RPCs.
type promptStopper struct{ stops atomic.Int32 }

func (p *promptStopper) GracefulStop() {}
func (p *promptStopper) Stop()         { p.stops.Add(1) }

// TestStopGRPCWithinForcesStopWhenGracefulHangs pins the fix for the shutdown
// half of the eviction bug class: an unbounded GracefulStop with a task's log
// stream open burned the whole terminationGracePeriodSeconds and the kubelet
// SIGKILLed the pod, so no deferred cleanup ran. The stop must return within
// its bound, taking the Stop fallback and reporting that it did.
func TestStopGRPCWithinForcesStopWhenGracefulHangs(t *testing.T) {
	srv := newHangingStopper()
	start := time.Now()
	forced := stopGRPCWithin(srv, 100*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("stopGRPCWithin took %s with a hanging GracefulStop; must complete within the bound", took)
	}
	if !forced {
		t.Error("forced = false, want true when the graceful stop exceeded its bound")
	}
	if srv.stops.Load() != 1 {
		t.Errorf("Stop called %d times, want exactly once", srv.stops.Load())
	}
}

// TestStopGRPCWithinReturnsPromptlyWhenGracefulCompletes: with no in-flight
// RPCs the graceful path wins and Stop is never forced.
func TestStopGRPCWithinReturnsPromptlyWhenGracefulCompletes(t *testing.T) {
	srv := &promptStopper{}
	if forced := stopGRPCWithin(srv, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil))); forced {
		t.Error("forced = true, want false when GracefulStop completes on its own")
	}
	if srv.stops.Load() != 0 {
		t.Errorf("Stop called %d times, want 0", srv.stops.Load())
	}
}

// blockingAgentService is an AgentService whose StreamLogs behaves like the real
// one with a task still running: it reads one line, then blocks on Recv for as
// long as the stream lives. Its deferred cleanup stands in for the log writer's
// Close (the flush), and the test asserts it ran before the stop returned.
type blockingAgentService struct {
	agentv1.UnimplementedAgentServiceServer
	started chan struct{}
	mu      sync.Mutex
	cleaned bool
}

func (b *blockingAgentService) StreamLogs(stream agentv1.AgentService_StreamLogsServer) error {
	defer func() {
		b.mu.Lock()
		b.cleaned = true
		b.mu.Unlock()
	}()
	if _, err := stream.Recv(); err != nil {
		return err
	}
	close(b.started)
	for {
		if _, err := stream.Recv(); err != nil {
			return err
		}
	}
}

func (b *blockingAgentService) cleanedUp() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cleaned
}

// TestStopGRPCWithinRealServerRunsHandlerCleanup drives a real grpc.Server with
// a stream a client holds open. The forced Stop must both complete within the
// bound and let the handler's deferred cleanup run before returning — that is
// what turns "SIGKILL with the flush skipped" into "flushed, then exit".
func TestStopGRPCWithinRealServerRunsHandlerCleanup(t *testing.T) {
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	svc := &blockingAgentService{started: make(chan struct{})}
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, svc)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := agentv1.NewAgentServiceClient(conn).StreamLogs(ctx)
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	if serr := stream.Send(&agentv1.LogLine{Message: "hello"}); serr != nil {
		t.Fatalf("Send: %v", serr)
	}
	select {
	case <-svc.started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never received the first line")
	}

	start := time.Now()
	forced := stopGRPCWithin(srv, 200*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("stopGRPCWithin took %s with an open stream; must complete within the bound", took)
	}
	if !forced {
		t.Error("forced = false, want true: the client never ended its stream")
	}
	if !svc.cleanedUp() {
		t.Fatal("handler's deferred cleanup did not run before the stop returned: a real flush would have been skipped")
	}
}
