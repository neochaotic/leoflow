package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// caPool builds a certificate pool trusting the PEM CA at path, for verifying a
// self-signed / cluster server certificate.
func caPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path) //nolint:gosec // G304: CA path is operator-supplied config.
	if err != nil {
		return nil, fmt.Errorf("reading CA file %q: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates found in CA file %q", path)
	}
	return pool, nil
}

// Dial connects to the control plane's AgentService, attaching the bearer token
// to every RPC. When allowInsecure is true (local development against a cluster
// without TLS) the transport is unencrypted; otherwise TLS 1.2+ is required. When
// caFile is set, the server certificate is verified against that CA (a
// self-signed / cluster CA); otherwise the system roots are used.
func Dial(addr, token string, allowInsecure bool, caFile string) (agentv1.AgentServiceClient, *grpc.ClientConn, error) {
	if addr == "" {
		return nil, nil, errors.New("control plane address is required")
	}
	if token == "" {
		return nil, nil, errors.New("agent token is required")
	}

	transport := insecure.NewCredentials()
	if !allowInsecure {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if caFile != "" {
			pool, cerr := caPool(caFile)
			if cerr != nil {
				return nil, nil, cerr
			}
			tlsCfg.RootCAs = pool
		}
		transport = credentials.NewTLS(tlsCfg)
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
