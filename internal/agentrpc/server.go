// Package agentrpc implements the control-plane side of the agent gRPC protocol:
// it authenticates each in-pod agent by its per-task-instance token, serves the
// task specification, and records the state transitions the agent reports.
package agentrpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/logs"
	"github.com/neochaotic/leoflow/internal/xcom"
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
	XComSchema       map[string]any
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

// XComService stores and retrieves XCom values for the agent.
type XComService interface {
	Push(ctx context.Context, key xcom.Key, value []byte, contentType string, schema map[string]any) error
	Fetch(ctx context.Context, key xcom.Key) (xcom.Entry, error)
}

// LogSink opens a writer for a task attempt's streamed logs.
type LogSink interface {
	Open(ref logs.Ref) (logs.LogWriter, error)
}

// LogPublisher fans a log line out for live tailing (optional).
type LogPublisher interface {
	Publish(ctx context.Context, ref logs.Ref, line string) error
}

// Server implements agentv1.AgentServiceServer over a Store and Authenticator.
type Server struct {
	agentv1.UnimplementedAgentServiceServer
	auth  Authenticator
	store Store
	xcom  XComService
	logs  LogSink
	tail  LogPublisher
	now   func() time.Time
}

// NewServer builds an AgentService server backed by the given authenticator,
// store, and XCom service.
func NewServer(authn Authenticator, store Store, xcomSvc XComService) *Server {
	return &Server{auth: authn, store: store, xcom: xcomSvc, now: time.Now}
}

// SetLogSink attaches the log sink that StreamLogs writes to. Without it,
// StreamLogs reports Unimplemented.
func (s *Server) SetLogSink(sink LogSink) { s.logs = sink }

// SetLogPublisher attaches the live-tail publisher (optional). When set,
// StreamLogs publishes each line for the UI's live tail.
func (s *Server) SetLogPublisher(p LogPublisher) { s.tail = p }

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

// PushXCom stores a value the task produced, keyed by the caller's identity.
// Size/schema violations are returned as a rejection, not a transport error, so
// the agent can fail the task with a clear reason.
func (s *Server) PushXCom(ctx context.Context, req *agentv1.PushXComRequest) (*agentv1.PushXComResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	spec, err := s.store.TaskSpec(ctx, *id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "loading task spec: %v", err)
	}
	key := xcomKey(*id, id.TaskID, req.GetKey())
	perr := s.xcom.Push(ctx, key, req.GetValue(), req.GetContentType(), spec.XComSchema)
	switch {
	case errors.Is(perr, xcom.ErrTooLarge):
		return &agentv1.PushXComResponse{Accepted: false, RejectionReason: "payload_too_large"}, nil
	case errors.Is(perr, xcom.ErrSchemaMismatch):
		return &agentv1.PushXComResponse{Accepted: false, RejectionReason: "schema_mismatch"}, nil
	case perr != nil:
		return nil, status.Errorf(codes.Internal, "storing xcom: %v", perr)
	}
	return &agentv1.PushXComResponse{Accepted: true}, nil
}

// FetchXCom returns an upstream task's value, but only from a task the caller
// declared as an XCom input within the same run (and, by construction, the same
// tenant), enforcing cross-tenant and cross-run isolation.
func (s *Server) FetchXCom(ctx context.Context, req *agentv1.FetchXComRequest) (*agentv1.FetchXComResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	spec, err := s.store.TaskSpec(ctx, *id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "loading task spec: %v", err)
	}
	if !declaresUpstream(spec.XComInputMapping, req.GetUpstreamTaskId()) {
		return nil, status.Errorf(codes.PermissionDenied, "task %q did not declare %q as an xcom input", id.TaskID, req.GetUpstreamTaskId())
	}
	entry, err := s.xcom.Fetch(ctx, xcomKey(*id, req.GetUpstreamTaskId(), req.GetKey()))
	if errors.Is(err, xcom.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "no xcom for task %q", req.GetUpstreamTaskId())
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "reading xcom: %v", err)
	}
	return &agentv1.FetchXComResponse{
		Value:       entry.Value,
		ContentType: entry.ContentType,
		SizeBytes:   clampInt32(entry.SizeBytes),
		CreatedAt:   timestamppb.New(entry.CreatedAt),
	}, nil
}

// StreamLogs receives the task's log lines and writes them through the sink,
// flushing on stream end so the logs survive the pod.
func (s *Server) StreamLogs(stream agentv1.AgentService_StreamLogsServer) (err error) {
	id, ierr := s.identify(stream.Context())
	if ierr != nil {
		return ierr
	}
	if s.logs == nil {
		return status.Error(codes.Unimplemented, "log shipping is not configured")
	}
	w, oerr := s.logs.Open(logs.Ref{
		TenantID: id.TenantID, DagID: id.DagID, RunID: id.RunID, TaskID: id.TaskID, TryNumber: id.TryNumber,
	})
	if oerr != nil {
		// Surface the cause: without this, a non-writable logs.dir makes the
		// agent see only a bare stream EOF, with no server-side explanation (#36).
		slog.Error("opening log sink for task; logs will not be shipped",
			"dag", id.DagID, "run", id.RunID, "task", id.TaskID, "error", oerr)
		return status.Errorf(codes.Internal, "opening log sink: %v", oerr)
	}
	defer func() {
		if cerr := w.Close(); cerr != nil && err == nil {
			err = status.Errorf(codes.Internal, "flushing logs: %v", cerr)
		}
	}()

	ref := logs.Ref{TenantID: id.TenantID, DagID: id.DagID, RunID: id.RunID, TaskID: id.TaskID, TryNumber: id.TryNumber}
	publish := func(string) {}
	if s.tail != nil {
		publish = func(line string) {
			if perr := s.tail.Publish(stream.Context(), ref, line); perr != nil {
				slog.Warn("publishing log tail", "task", id.TaskID, "error", perr)
			}
		}
	}
	return writeLines(w, stream.Recv, publish)
}

// writeLines drains log lines from recv into the writer until the stream ends,
// also publishing each line for live tailing.
func writeLines(w logs.LogWriter, recv func() (*agentv1.LogLine, error), publish func(string)) error {
	for {
		line, err := recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return status.Errorf(codes.Internal, "receiving log line: %v", err)
		}
		msg := line.GetMessage()
		if werr := w.WriteLine(msg); werr != nil {
			return status.Errorf(codes.Internal, "writing log line: %v", werr)
		}
		publish(msg)
	}
}

// xcomKey builds the XCom key for a task within the caller's tenant/dag/run.
func xcomKey(id auth.AgentIdentity, taskID, name string) xcom.Key {
	if name == "" {
		name = "return_value"
	}
	return xcom.Key{TenantID: id.TenantID, DagID: id.DagID, RunID: id.RunID, TaskID: taskID, Name: name}
}

// declaresUpstream reports whether the task declared upstreamTaskID as an input.
func declaresUpstream(mapping map[string]string, upstreamTaskID string) bool {
	for _, declared := range mapping {
		if declared == upstreamTaskID {
			return true
		}
	}
	return false
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
