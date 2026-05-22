package agentrpc

import (
	"context"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeStore records ReportState calls and serves a fixed task spec.
type fakeStore struct {
	spec       TaskSpec
	specErr    error
	reported   []reportedState
	reportErr  error
}

type reportedState struct {
	id       auth.AgentIdentity
	state    domain.TaskState
	exitCode int
	errMsg   string
}

func (s *fakeStore) TaskSpec(context.Context, auth.AgentIdentity) (TaskSpec, error) {
	return s.spec, s.specErr
}

func (s *fakeStore) ReportState(_ context.Context, id auth.AgentIdentity, st domain.TaskState, exit int, msg string) error {
	s.reported = append(s.reported, reportedState{id, st, exit, msg})
	return s.reportErr
}

func testIdentity() auth.AgentIdentity {
	return auth.AgentIdentity{
		TaskInstanceID: "ti-1", TenantID: "acme", DagID: "etl",
		RunID: "run-1", TaskID: "extract", TryNumber: 1,
	}
}

// ctxWithToken builds an incoming context carrying a freshly minted agent token.
func ctxWithToken(t *testing.T, a *auth.JWTAuthenticator) context.Context {
	t.Helper()
	token, err := a.IssueAgentToken(testIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewIncomingContext(context.Background(), md)
}

func newServer(store Store) (*Server, *auth.JWTAuthenticator) {
	a := auth.NewJWTAuthenticator(nil, "secret", time.Hour)
	return NewServer(a, store), a
}

func TestGetTaskSpecMapsStoreData(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{
		Operator: "python", Entrypoint: "dag:hello", DagVersion: "v1",
		Environment:      map[string]string{"FOO": "bar"},
		XComInputMapping: map[string]string{"val": "upstream"},
		TimeoutSeconds:   300,
	}}
	srv, a := newServer(store)

	spec, err := srv.GetTaskSpec(ctxWithToken(t, a), &agentv1.GetTaskSpecRequest{})
	if err != nil {
		t.Fatalf("GetTaskSpec: %v", err)
	}
	if spec.GetOperator() != "python" || spec.GetEntrypoint() != "dag:hello" {
		t.Errorf("operator/entrypoint = %q/%q", spec.GetOperator(), spec.GetEntrypoint())
	}
	if spec.GetTaskId() != "extract" || spec.GetRunId() != "run-1" || spec.GetTryNumber() != 1 {
		t.Errorf("identity not propagated: %+v", spec)
	}
	if spec.GetEnvironment()["FOO"] != "bar" {
		t.Errorf("environment not mapped: %v", spec.GetEnvironment())
	}
	if spec.GetXcomInputMapping()["val"] != "upstream" {
		t.Errorf("xcom mapping not mapped: %v", spec.GetXcomInputMapping())
	}
	if spec.GetExecutionTimeoutSeconds() != 300 {
		t.Errorf("timeout = %d", spec.GetExecutionTimeoutSeconds())
	}
}

func TestRPCsRejectMissingToken(t *testing.T) {
	srv, _ := newServer(&fakeStore{})
	if _, err := srv.GetTaskSpec(context.Background(), &agentv1.GetTaskSpecRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("missing token: code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestRPCsRejectInvalidToken(t *testing.T) {
	srv, _ := newServer(&fakeStore{})
	md := metadata.Pairs("authorization", "Bearer not-a-token")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	if _, err := srv.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("invalid token: code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestReportStateAppliesTransition(t *testing.T) {
	store := &fakeStore{}
	srv, a := newServer(store)

	_, err := srv.ReportState(ctxWithToken(t, a), &agentv1.ReportStateRequest{
		State:    agentv1.TaskState_TASK_STATE_SUCCESS,
		ExitCode: 0,
	})
	if err != nil {
		t.Fatalf("ReportState: %v", err)
	}
	if len(store.reported) != 1 {
		t.Fatalf("expected one reported state, got %d", len(store.reported))
	}
	got := store.reported[0]
	if got.state != domain.TaskStateSuccess {
		t.Errorf("state = %q, want success", got.state)
	}
	if got.id.TaskInstanceID != "ti-1" {
		t.Errorf("identity = %+v", got.id)
	}
}

func TestReportStateRejectsUnknownState(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	if _, err := srv.ReportState(ctxWithToken(t, a), &agentv1.ReportStateRequest{
		State: agentv1.TaskState_TASK_STATE_UNSPECIFIED,
	}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("unspecified state: code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestRegisterAndHeartbeatReturnServerTime(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	ctx := ctxWithToken(t, a)

	reg, err := srv.Register(ctx, &agentv1.RegisterRequest{AgentVersion: "test"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if reg.GetSessionId() == "" || reg.GetServerTime() == nil {
		t.Errorf("register response incomplete: %+v", reg)
	}
	hb, err := srv.Heartbeat(ctx, &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if hb.GetServerTime() == nil {
		t.Error("heartbeat should return server time")
	}
}
