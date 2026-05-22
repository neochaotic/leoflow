package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"

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

// grpcLogSink adapts the StreamLogs bidirectional stream to the LogSink interface.
type grpcLogSink struct {
	stream grpc.BidiStreamingClient[agentv1.LogLine, agentv1.LogAck]
}

// OpenLogSink starts the StreamLogs RPC and returns a sink that forwards lines to it.
func OpenLogSink(ctx context.Context, client agentv1.AgentServiceClient) (LogSink, error) {
	stream, err := client.StreamLogs(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening log stream: %w", err)
	}
	return &grpcLogSink{stream: stream}, nil
}

// Send forwards a single log line to the control plane.
func (s *grpcLogSink) Send(line *agentv1.LogLine) error { return s.stream.Send(line) }

// Close signals the control plane that no more log lines will be sent.
func (s *grpcLogSink) Close() error { return s.stream.CloseSend() }
