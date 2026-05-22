// Package agentrpc implements the control-plane side of the agent gRPC protocol:
// it authenticates each in-pod agent by its per-task-instance token, serves the
// task specification, and records the state transitions the agent reports.
package agentrpc

import (
	"context"
	"strings"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TaskSpec is the execution specification the agent needs to run a task.
type TaskSpec struct {
	Operator         string
	Entrypoint       string
	DagVersion       string
	Environment      map[string]string
	XComInputMapping map[string]string
	TimeoutSeconds   int
}

// Authenticator verifies an agent bearer token into a task instance identity.
type Authenticator interface {
	AuthenticateAgent(token string) (*auth.AgentIdentity, error)
}

// Store is the server's view of persistent task state.
type Store interface {
	// TaskSpec returns the execution spec for the identified task instance.
	TaskSpec(ctx context.Context, id auth.AgentIdentity) (TaskSpec, error)
	// ReportState records a state transition reported by the agent.
	ReportState(ctx context.Context, id auth.AgentIdentity, state domain.TaskState, exitCode int, errMsg string) error
}

// Server implements agentv1.AgentServiceServer over a Store and Authenticator.
type Server struct {
	agentv1.UnimplementedAgentServiceServer
	auth  Authenticator
	store Store
	now   func() time.Time
}

// NewServer builds an AgentService server backed by the given authenticator and store.
func NewServer(authn Authenticator, store Store) *Server {
	return &Server{auth: authn, store: store, now: time.Now}
}

// Register acknowledges an agent's startup and returns the server clock.
func (s *Server) Register(ctx context.Context, _ *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	return &agentv1.RegisterResponse{
		SessionId:  id.TaskInstanceID,
		ServerTime: timestamppb.New(s.now()),
	}, nil
}

// GetTaskSpec returns the execution spec for the calling task instance.
func (s *Server) GetTaskSpec(ctx context.Context, _ *agentv1.GetTaskSpecRequest) (*agentv1.TaskSpec, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	spec, err := s.store.TaskSpec(ctx, *id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "loading task spec: %v", err)
	}
	return &agentv1.TaskSpec{
		TenantId:                id.TenantID,
		DagId:                   id.DagID,
		DagVersion:              spec.DagVersion,
		RunId:                   id.RunID,
		TaskId:                  id.TaskID,
		TryNumber:               clampInt32(id.TryNumber),
		Operator:                spec.Operator,
		Entrypoint:              spec.Entrypoint,
		Environment:             spec.Environment,
		XcomInputMapping:        spec.XComInputMapping,
		ExecutionTimeoutSeconds: clampInt32(spec.TimeoutSeconds),
	}, nil
}

// ReportState records a state transition the agent observed for its task.
func (s *Server) ReportState(ctx context.Context, req *agentv1.ReportStateRequest) (*agentv1.ReportStateResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	state, err := mapState(req.GetState())
	if err != nil {
		return nil, err
	}
	if rerr := s.store.ReportState(ctx, *id, state, int(req.GetExitCode()), req.GetErrorMessage()); rerr != nil {
		return nil, status.Errorf(codes.Internal, "recording state: %v", rerr)
	}
	return &agentv1.ReportStateResponse{Acknowledged: true}, nil
}

// Heartbeat returns the server clock so the agent can detect skew.
func (s *Server) Heartbeat(ctx context.Context, _ *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	if _, err := s.identify(ctx); err != nil {
		return nil, err
	}
	return &agentv1.HeartbeatResponse{ServerTime: timestamppb.New(s.now())}, nil
}

// identify extracts and verifies the agent token from the request metadata.
func (s *Server) identify(ctx context.Context) (*auth.AgentIdentity, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing request metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing authorization token")
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	id, err := s.auth.AuthenticateAgent(token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid agent token")
	}
	return id, nil
}

// mapState translates a protobuf task state into the domain vocabulary.
func mapState(state agentv1.TaskState) (domain.TaskState, error) {
	switch state {
	case agentv1.TaskState_TASK_STATE_RUNNING:
		return domain.TaskStateRunning, nil
	case agentv1.TaskState_TASK_STATE_SUCCESS:
		return domain.TaskStateSuccess, nil
	case agentv1.TaskState_TASK_STATE_FAILED:
		return domain.TaskStateFailed, nil
	case agentv1.TaskState_TASK_STATE_SKIPPED:
		return domain.TaskStateSkipped, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "unsupported task state %v", state)
	}
}

// clampInt32 narrows a non-negative count to int32 for the wire protocol.
func clampInt32(n int) int32 {
	if n < 0 {
		return 0
	}
	if n > 1<<31-1 {
		return 1<<31 - 1
	}
	return int32(n)
}
