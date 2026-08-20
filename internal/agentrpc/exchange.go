package agentrpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/neochaotic/leoflow/internal/auth"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

// ReviewedPod is the pod a validated projected ServiceAccount token identifies.
// The concrete TokenReviewer fills it from the Kubernetes TokenReview response —
// the bound-token status carries the pod name/uid the token was issued for. The
// field names the control plane resolves a task instance from are
// apiserver-version-dependent (ADR 0055 D7): the primary key is PodName in the
// task namespace; PodUID guards against a name-reused stale pod.
type ReviewedPod struct {
	Namespace      string
	PodName        string
	PodUID         string
	ServiceAccount string
}

// TokenReviewer validates a projected ServiceAccount token against the control
// plane's audience via the Kubernetes TokenReview API and returns the pod it was
// issued for. It is the ONE apiserver call in the exchange, made once per pod at
// bootstrap (never on the secret hot path). It is an interface so it is MOCKED in
// unit tests — the concrete client needs a real apiserver and is exercised only
// by the owed real-cluster e2e. An error (bad signature, expired, wrong audience,
// or authenticated=false) means the token is not a valid bootstrap credential.
type TokenReviewer interface {
	ReviewProjectedToken(ctx context.Context, token string) (ReviewedPod, error)
}

// PodTaskResolver maps a reviewed pod to the task-instance identity it runs, so
// the minted JWT is scoped to the attempt (the identity Fix #1 filters on and
// Fix #2 liveness-checks). It is an interface so it is mocked in unit tests. The
// concrete resolver reads the identity the control plane itself stamped on the
// pod object at dispatch, keyed on the apiserver-validated pod reference.
type PodTaskResolver interface {
	ResolveTaskInstance(ctx context.Context, pod ReviewedPod) (auth.AgentIdentity, error)
}

// AgentTokenMinter issues a task-scoped agent JWT for a resolved identity. It is
// satisfied by *auth.JWTAuthenticator (IssueAgentToken) — the same minter used at
// dispatch and heartbeat renewal, so the exchanged token is indistinguishable
// from a dispatched one on every downstream path.
type AgentTokenMinter interface {
	IssueAgentToken(id auth.AgentIdentity, ttl time.Duration) (string, error)
}

// SetTokenExchange wires the projected-SA-token exchange (ADR 0055 Fix #3): the
// TokenReview client, the pod→task-instance resolver, the JWT minter, and the TTL
// of the minted task-scoped token. allowInsecure permits running the exchange
// over a non-TLS channel (dev only); production must use TLS (ExchangeToken fails
// closed otherwise, like the secret path). A nil reviewer leaves the exchange OFF
// — ExchangeToken then reports Unimplemented — which is the default (env-var)
// transport, so a deployment that does not opt in is byte-identical to today.
func (s *Server) SetTokenExchange(reviewer TokenReviewer, resolver PodTaskResolver, minter AgentTokenMinter, ttl time.Duration, allowInsecure bool) {
	s.reviewer = reviewer
	s.podResolver = resolver
	s.tokenMinter = minter
	s.exchangeTTL = ttl
	s.allowInsecureExchange = allowInsecure
}

// ExchangeToken validates the agent's projected ServiceAccount token (bootstrap
// bearer), resolves the pod to its task instance, and returns a freshly minted
// task-scoped agent JWT (ADR 0055 Fix #3). It is called ONCE at agent startup
// under the exchange transport; the default env-var transport never calls it.
//
// It fails closed at every step: Unimplemented when the exchange is not wired,
// PermissionDenied on an insecure channel, Unauthenticated on a missing or
// rejected projected token, and Internal when the reviewed pod cannot be resolved
// to an attempt (never mint an unscoped or misattributed token). The minted token
// and the presented projected token are never logged.
func (s *Server) ExchangeToken(ctx context.Context, _ *agentv1.ExchangeTokenRequest) (*agentv1.ExchangeTokenResponse, error) {
	if s.reviewer == nil || s.podResolver == nil || s.tokenMinter == nil {
		return nil, status.Error(codes.Unimplemented, "token exchange is not enabled")
	}
	if gerr := s.guardExchangeChannel(ctx); gerr != nil {
		return nil, gerr
	}
	token, ok := bearerFromContext(ctx)
	if !ok || token == "" {
		return nil, status.Error(codes.Unauthenticated, "missing projected token")
	}
	pod, err := s.reviewer.ReviewProjectedToken(ctx, token)
	if err != nil {
		// A rejected projected token is not a server fault: the credential is
		// invalid, expired, or carries the wrong audience. Surface Unauthenticated
		// without echoing the token or the review detail to the agent.
		slog.Warn("rejecting agent token exchange: projected token review failed", "error", err)
		return nil, status.Error(codes.Unauthenticated, "projected token is not valid")
	}
	id, err := s.podResolver.ResolveTaskInstance(ctx, pod)
	if err != nil {
		slog.Warn("agent token exchange: cannot resolve reviewed pod to a task instance",
			"namespace", pod.Namespace, "pod", pod.PodName, "pod_uid", pod.PodUID, "error", err)
		return nil, status.Errorf(codes.Internal, "resolving pod to task instance: %v", err)
	}
	minted, err := s.tokenMinter.IssueAgentToken(id, s.exchangeTTL)
	if err != nil {
		slog.Warn("agent token exchange: minting task-scoped token failed",
			"ti", id.TaskInstanceID, "run", id.RunID, "task", id.TaskID, "try", id.TryNumber, "error", err)
		return nil, status.Errorf(codes.Internal, "minting task-scoped token: %v", err)
	}
	slog.Info("exchanged projected token for a task-scoped agent JWT",
		"ti", id.TaskInstanceID, "tenant", id.TenantID, "dag", id.DagID,
		"run", id.RunID, "task", id.TaskID, "try", id.TryNumber, "namespace", pod.Namespace, "pod", pod.PodName)
	return &agentv1.ExchangeTokenResponse{AgentToken: minted}, nil
}

// guardExchangeChannel refuses to run the exchange over a plaintext channel
// unless explicitly allowed for dev — the exchange returns a bearer credential,
// so it must not transit an insecure transport by default. Mirrors the secret
// channel guard.
func (s *Server) guardExchangeChannel(ctx context.Context) error {
	if s.allowInsecureExchange || channelIsSecure(ctx) {
		return nil
	}
	return status.Error(codes.PermissionDenied,
		"refusing to exchange a token over an insecure channel; enable gRPC TLS (see issue #58)")
}
