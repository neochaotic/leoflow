package agentrpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
)

// TestInflightHandlersCountsUnary: the count rises for the duration of a unary
// handler and returns to zero once it has returned.
func TestInflightHandlersCountsUnary(t *testing.T) {
	c := NewInflightHandlers()
	if got := c.Count(); got != 0 {
		t.Fatalf("Count() on a fresh counter = %d, want 0", got)
	}
	icpt := c.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/agent.v1.AgentService/Register"}

	inside := -1
	if _, err := icpt(context.Background(), "req", info, func(context.Context, any) (any, error) {
		inside = c.Count()
		return "ok", nil
	}); err != nil {
		t.Fatalf("interceptor error = %v", err)
	}
	if inside != 1 {
		t.Errorf("Count() inside the handler = %d, want 1", inside)
	}
	if got := c.Count(); got != 0 {
		t.Errorf("Count() after the handler returned = %d, want 0", got)
	}
}

// TestInflightHandlersCountsStream is the case the shutdown path cares about:
// StreamLogs and AwaitAssignment are the handlers that outlive their RPC's
// arrival, so the count they contribute is what the bounded stop reports.
func TestInflightHandlersCountsStream(t *testing.T) {
	c := NewInflightHandlers()
	icpt := c.StreamInterceptor()
	info := &grpc.StreamServerInfo{FullMethod: "/agent.v1.AgentService/StreamLogs"}

	inside := -1
	if err := icpt(nil, fakeStream{}, info, func(any, grpc.ServerStream) error {
		inside = c.Count()
		return nil
	}); err != nil {
		t.Fatalf("interceptor error = %v", err)
	}
	if inside != 1 {
		t.Errorf("Count() inside the stream handler = %d, want 1", inside)
	}
	if got := c.Count(); got != 0 {
		t.Errorf("Count() after the stream handler returned = %d, want 0", got)
	}
}

// TestInflightHandlersDecrementsOnFailure: a handler that returns an error, or
// panics, must still be decremented — a counter that leaks on the failure paths
// would report phantom handlers on every later shutdown, which is worse than
// reporting none.
func TestInflightHandlersDecrementsOnFailure(t *testing.T) {
	c := NewInflightHandlers()
	unary := c.UnaryInterceptor()
	if _, err := unary(context.Background(), "req", &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		return nil, errors.New("handler failed")
	}); err == nil {
		t.Fatal("interceptor swallowed the handler's error")
	}
	if got := c.Count(); got != 0 {
		t.Errorf("Count() after a failed handler = %d, want 0", got)
	}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("the counter must not swallow the panic; the recovery interceptor owns that")
			}
		}()
		_, _ = unary(context.Background(), "req", &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
			panic("boom")
		})
	}()
	if got := c.Count(); got != 0 {
		t.Errorf("Count() after a panicking handler = %d, want 0", got)
	}
}

// TestInflightHandlersCountsConcurrentHandlers: the shutdown log is only useful
// if it counts every handler that is open at once.
func TestInflightHandlersCountsConcurrentHandlers(t *testing.T) {
	c := NewInflightHandlers()
	icpt := c.StreamInterceptor()
	info := &grpc.StreamServerInfo{FullMethod: "/agent.v1.AgentService/StreamLogs"}

	release := make(chan struct{})
	entered := make(chan int, 3)
	for range 3 {
		go func() {
			_ = icpt(nil, fakeStream{}, info, func(any, grpc.ServerStream) error {
				entered <- c.Count()
				<-release
				return nil
			})
		}()
	}
	for range 3 {
		<-entered
	}
	if got := c.Count(); got != 3 {
		t.Errorf("Count() with three open streams = %d, want 3", got)
	}
	close(release)
}
