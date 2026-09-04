package agentrpc

import (
	"context"
	"sync/atomic"

	"google.golang.org/grpc"
)

// InflightHandlers counts the agent RPC handlers currently executing. gRPC
// exposes no such number, and the bounded graceful stop needs it: past its
// bound the process force-closes the transports and waits only briefly for the
// handlers to return, while a log writer's final Put is bounded far longer, so
// the process can legitimately exit with handlers still running. Abandoning one
// is safe — a log object is written by a single atomic Put, so the stored object
// stays at its previous flush rather than being truncated — but silent, and
// this count is what makes it visible.
//
// The zero value is not usable; build one with NewInflightHandlers. It is safe
// for concurrent use.
type InflightHandlers struct {
	n atomic.Int64
}

// NewInflightHandlers returns a zeroed counter to install on a gRPC server via
// UnaryInterceptor and StreamInterceptor and to read with Count.
func NewInflightHandlers() *InflightHandlers { return &InflightHandlers{} }

// Count reports how many handlers are executing right now.
func (c *InflightHandlers) Count() int { return int(c.n.Load()) }

// UnaryInterceptor counts a unary handler for the duration of its call.
func (c *InflightHandlers) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		c.n.Add(1)
		// Deferred so an error return AND a panic both decrement: a counter that
		// leaked on the failure paths would report phantom handlers on every later
		// shutdown, which is worse than reporting none. The panic itself belongs to
		// the recovery interceptor, so it is deliberately not recovered here.
		defer c.n.Add(-1)
		return handler(ctx, req)
	}
}

// StreamInterceptor counts a streaming handler for the duration of its stream.
// These are the handlers the shutdown log is about: StreamLogs lives as long as
// its task and an idle warm worker's AwaitAssignment lives indefinitely, so
// they are what is still open when the bounded stop gives up.
func (c *InflightHandlers) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		c.n.Add(1)
		defer c.n.Add(-1)
		return handler(srv, ss)
	}
}
