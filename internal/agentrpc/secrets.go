package agentrpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/neochaotic/leoflow/internal/auth"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

// SecretsStore returns a tenant's Variables and Connections for delivery to a
// task pod (ADR 0021). Connection URIs carry decrypted credentials, so this is
// only ever served over the authenticated agent channel — never to the UI/API.
type SecretsStore interface {
	SecretVariables(ctx context.Context, tenant string) (map[string]string, error)
	SecretConnectionURIs(ctx context.Context, tenant string) (map[string]string, error)
}

// SecretScopeAuditor records a structured audit event when a task receives the
// full tenant secret set despite declaring only a subset of it — the visibility
// half of the warn-before-enforce arc (ADR 0045 §Settled #3, ADR 0055). It
// carries counts only, never secret names or values. It is optional and
// best-effort: a nil auditor or a write error only skips the audit row; it never
// changes what is delivered.
type SecretScopeAuditor interface {
	RecordSecretScopeWarning(ctx context.Context, tenant, dagID, runID, taskID, kind string, declared, total int) error
}

// SetSecrets attaches the secrets store. allowInsecure permits serving secrets
// over a non-TLS channel — for local/dev only; production must use TLS (the
// handlers fail closed otherwise). See ADR 0021 / issue #58.
func (s *Server) SetSecrets(store SecretsStore, allowInsecure bool) {
	s.secrets, s.allowInsecureSecrets = store, allowInsecure
}

// SetSecretScopeAuditor attaches the sink for secret-scope warning events
// (optional). Without it, a narrowing declaration still produces the WARN log
// but no audit row.
func (s *Server) SetSecretScopeAuditor(a SecretScopeAuditor) { s.secretAudit = a }

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
	s.warnIfNarrowerScope(ctx, id, "variables", vars)
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
	s.warnIfNarrowerScope(ctx, id, "connections", uris)
	return &agentv1.GetConnectionsResponse{ConnectionUris: uris}, nil
}

// warnIfNarrowerScope emits a structured WARN log and a best-effort audit event
// when the calling task declared a non-empty secret set that is a strict subset
// of the full tenant set it is about to receive (ADR 0045 §Settled #3, ADR 0055).
// It never changes what is delivered — delivery stays fetch-all in the warn phase
// — and it records counts only, never secret names or values. kind is
// "variables" or "connections". A task that declared nothing is the permissive
// default and is not warned.
func (s *Server) warnIfNarrowerScope(ctx context.Context, id *auth.AgentIdentity, kind string, delivered map[string]string) {
	// The declared set lives on the task spec (ADR 0045). Loading it is only for
	// the warning; a load failure must never affect delivery, which already
	// succeeded, so log and return without warning.
	spec, err := s.store.TaskSpec(ctx, *id)
	if err != nil {
		slog.Warn("loading task spec for secret-scope warning; skipping warn",
			"ti", id.TaskInstanceID, "kind", kind, "error", err)
		return
	}
	declared := spec.DeclaredVariables
	if kind == "connections" {
		declared = spec.DeclaredConnections
	}
	declaredCount, deliveredCount, warn := scopeGap(declared, delivered)
	if !warn {
		return
	}
	slog.Warn("task received the full secret vault but declared a narrower set; under secret_scoping: enforce it would receive only its declared set",
		"ti", id.TaskInstanceID, "tenant", id.TenantID, "dag", id.DagID, "run", id.RunID,
		"task", id.TaskID, "kind", kind, "declared", declaredCount, "delivered", deliveredCount)
	if s.secretAudit == nil {
		return
	}
	if aerr := s.secretAudit.RecordSecretScopeWarning(ctx, id.TenantID, id.DagID, id.RunID, id.TaskID, kind, declaredCount, deliveredCount); aerr != nil {
		slog.Warn("recording secret-scope warning audit event",
			"ti", id.TaskInstanceID, "kind", kind, "error", aerr)
	}
}

// scopeGap compares a task's declared secret names against the full tenant set it
// is about to receive. It returns the count of declared names that are actually
// delivered, the delivered total, and whether that declaration is a strict subset
// (non-empty and covering fewer than all delivered secrets) — the condition the
// warn phase records. E1a rejects declaring a name that does not exist, so a
// declared name is expected to be delivered; a name that is not is simply not
// counted, keeping the comparison robust.
func scopeGap(declared []string, delivered map[string]string) (declaredCount, deliveredCount int, strictSubset bool) {
	deliveredCount = len(delivered)
	if len(declared) == 0 {
		return 0, deliveredCount, false
	}
	seen := make(map[string]struct{}, len(declared))
	for _, name := range declared {
		if _, ok := delivered[name]; ok {
			seen[name] = struct{}{}
		}
	}
	declaredCount = len(seen)
	strictSubset = declaredCount > 0 && declaredCount < deliveredCount
	return declaredCount, deliveredCount, strictSubset
}
