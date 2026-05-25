package agentrpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRecoveryUnaryInterceptorRecoversPanic(t *testing.T) {
	icpt := RecoveryUnaryInterceptor(quietLogger())
	info := &grpc.UnaryServerInfo{FullMethod: "/agent.v1.Agent/ReportTaskResult"}

	// A panicking handler must not propagate; it returns Internal instead.
	resp, err := icpt(context.Background(), "req", info, func(context.Context, any) (any, error) {
		panic("boom in handler")
	})
	if resp != nil {
		t.Errorf("panicking handler should yield a nil response, got %v", resp)
	}
	if status.Code(err) != codes.Internal {
		t.Errorf("expected codes.Internal after a panic, got %v (%v)", status.Code(err), err)
	}
}

func TestRecoveryUnaryInterceptorPassesThrough(t *testing.T) {
	icpt := RecoveryUnaryInterceptor(quietLogger())
	info := &grpc.UnaryServerInfo{FullMethod: "/agent.v1.Agent/Ping"}

	resp, err := icpt(context.Background(), "req", info, func(_ context.Context, r any) (any, error) {
		return "ok:" + r.(string), nil
	})
	if err != nil || resp != "ok:req" {
		t.Errorf("a healthy handler must pass through unchanged, got resp=%v err=%v", resp, err)
	}
}

type fakeStream struct{ grpc.ServerStream }

func (fakeStream) Context() context.Context { return context.Background() }

func TestRecoveryStreamInterceptorRecoversPanic(t *testing.T) {
	icpt := RecoveryStreamInterceptor(quietLogger())
	info := &grpc.StreamServerInfo{FullMethod: "/agent.v1.Agent/StreamLogs"}

	err := icpt(nil, fakeStream{}, info, func(any, grpc.ServerStream) error {
		panic("boom mid-stream")
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("expected codes.Internal after a stream panic, got %v (%v)", status.Code(err), err)
	}
}

func TestRecoveryStreamInterceptorPassesThrough(t *testing.T) {
	icpt := RecoveryStreamInterceptor(quietLogger())
	info := &grpc.StreamServerInfo{FullMethod: "/agent.v1.Agent/StreamLogs"}
	sentinel := errors.New("handler result")
	err := icpt(nil, fakeStream{}, info, func(any, grpc.ServerStream) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Errorf("a healthy stream handler must pass through unchanged, got %v", err)
	}
}
