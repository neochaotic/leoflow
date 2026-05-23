package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Dial connects to the control plane's AgentService, attaching the bearer token
// to every RPC. When allowInsecure is true (local development against a cluster
// without TLS) the transport is unencrypted; otherwise TLS 1.2+ is required.
func Dial(addr, token string, allowInsecure bool) (agentv1.AgentServiceClient, *grpc.ClientConn, error) {
	if addr == "" {
		return nil, nil, errors.New("control plane address is required")
	}
	if token == "" {
		return nil, nil, errors.New("agent token is required")
	}

	transport := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	if allowInsecure {
		transport = insecure.NewCredentials()
	}

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(transport),
		grpc.WithPerRPCCredentials(tokenAuth{token: token, secure: !allowInsecure}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("dialing control plane at %q: %w", addr, err)
	}
	return agentv1.NewAgentServiceClient(conn), conn, nil
}

// grpcLogSink adapts the StreamLogs bidirectional stream to the LogSink
// interface. The task's stdout and stderr are copied on separate goroutines but
// share this one stream, so Send is serialized with a mutex: gRPC client streams
// do not permit concurrent SendMsg, and racing sends corrupt the stream (see
// #36).
type grpcLogSink struct {
	mu     sync.Mutex
	stream grpc.BidiStreamingClient[agentv1.LogLine, agentv1.LogAck]
}

// OpenLogSink starts the StreamLogs RPC and returns a sink that forwards lines
// to it. It is the agent's first RPC, so it uses WaitForReady: with the lazy
// connection of grpc.NewClient the channel may not be established yet, and
// without this the stream would fail fast on a cold connection (the "opening log
// stream" EOF in #36) rather than waiting for the control plane to be reachable.
func OpenLogSink(ctx context.Context, client agentv1.AgentServiceClient) (LogSink, error) {
	stream, err := client.StreamLogs(ctx, grpc.WaitForReady(true))
	if err != nil {
		return nil, fmt.Errorf("opening log stream: %w", err)
	}
	return &grpcLogSink{stream: stream}, nil
}

// Send forwards a single log line to the control plane, serialized so concurrent
// stdout/stderr writers never call the underlying stream's Send at once.
func (s *grpcLogSink) Send(line *agentv1.LogLine) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(line)
}

// Close signals the control plane that no more log lines will be sent.
func (s *grpcLogSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.CloseSend()
}
