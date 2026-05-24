package agentrpc

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

// SecretsStore returns a tenant's Variables and Connections for delivery to a
// task pod (ADR 0021). Connection URIs carry decrypted credentials, so this is
// only ever served over the authenticated agent channel — never to the UI/API.
type SecretsStore interface {
	SecretVariables(ctx context.Context, tenant string) (map[string]string, error)
	SecretConnectionURIs(ctx context.Context, tenant string) (map[string]string, error)
}

// SetSecrets attaches the secrets store. allowInsecure permits serving secrets
// over a non-TLS channel — for local/dev only; production must use TLS (the
// handlers fail closed otherwise). See ADR 0021 / issue #58.
func (s *Server) SetSecrets(store SecretsStore, allowInsecure bool) {
	s.secrets, s.allowInsecureSecrets = store, allowInsecure
}

// guardSecretChannel refuses to serve secrets when the store is unconfigured or
// the transport is not TLS (unless explicitly allowed for dev). This is the
// fail-closed gate: secrets never transit a plaintext channel by default.
func (s *Server) guardSecretChannel(ctx context.Context) error {
	if s.secrets == nil {
		return status.Error(codes.Unavailable, "secrets delivery is not configured")
	}
	if s.allowInsecureSecrets {
		return nil
	}
	if p, ok := peer.FromContext(ctx); ok && p.AuthInfo != nil {
		return nil // TLS (AuthInfo present) — secure
	}
	return status.Error(codes.PermissionDenied,
		"refusing to send secrets over an insecure channel; enable gRPC TLS (see issue #58)")
}

// GetVariables returns the calling task's tenant Variables for the agent to
// export as AIRFLOW_VAR_<KEY>.
func (s *Server) GetVariables(ctx context.Context, _ *agentv1.GetVariablesRequest) (*agentv1.GetVariablesResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	if gerr := s.guardSecretChannel(ctx); gerr != nil {
		return nil, gerr
	}
	vars, err := s.secrets.SecretVariables(ctx, id.TenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "fetching variables: %v", err)
	}
	return &agentv1.GetVariablesResponse{Variables: vars}, nil
}

// GetConnections returns the calling task's tenant Connections as Airflow URIs
// for the agent to export as AIRFLOW_CONN_<CONN_ID>.
func (s *Server) GetConnections(ctx context.Context, _ *agentv1.GetConnectionsRequest) (*agentv1.GetConnectionsResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	if gerr := s.guardSecretChannel(ctx); gerr != nil {
		return nil, gerr
	}
	uris, err := s.secrets.SecretConnectionURIs(ctx, id.TenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "fetching connections: %v", err)
	}
	return &agentv1.GetConnectionsResponse{ConnectionUris: uris}, nil
}
