package agentrpc

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/logs"
	"github.com/neochaotic/leoflow/internal/xcom"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeStore records ReportState calls and serves a fixed task spec.
type fakeStore struct {
	spec      TaskSpec
	specErr   error
	reported  []reportedState
	reportErr error
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

type fakeXCom struct {
	entries map[string]xcom.Entry
	pushErr error
}

func (x *fakeXCom) Push(_ context.Context, key xcom.Key, value []byte, ct string, _ map[string]any) error {
	if x.pushErr != nil {
		return x.pushErr
	}
	if x.entries == nil {
		x.entries = map[string]xcom.Entry{}
	}
	x.entries[key.String()] = xcom.Entry{Value: value, ContentType: ct}
	return nil
}

func (x *fakeXCom) Fetch(_ context.Context, key xcom.Key) (xcom.Entry, error) {
	if e, ok := x.entries[key.String()]; ok {
		return e, nil
	}
	return xcom.Entry{}, xcom.ErrNotFound
}

func newServer(store Store) (*Server, *auth.JWTAuthenticator) {
	return newServerX(store, &fakeXCom{})
}

func newServerX(store Store, x XComService) (*Server, *auth.JWTAuthenticator) {
	a := auth.NewJWTAuthenticator(nil, "secret", time.Hour)
	return NewServer(a, store, x), a
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

func TestPushXComStoresUnderIdentityKey(t *testing.T) {
	x := &fakeXCom{}
	srv, a := newServerX(&fakeStore{spec: TaskSpec{Operator: "python"}}, x)

	resp, err := srv.PushXCom(ctxWithToken(t, a), &agentv1.PushXComRequest{Value: []byte(`{"rows":1}`)})
	if err != nil {
		t.Fatalf("PushXCom: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("push not accepted: %s", resp.GetRejectionReason())
	}
	// identity = ti-1 / acme / etl / run-1 / extract; default key return_value.
	if _, ok := x.entries["xcom:acme:etl:run-1:extract:return_value"]; !ok {
		t.Errorf("stored keys = %v, want the identity-derived key", x.entries)
	}
}

func TestPushXComRejectsOversizeAndSchema(t *testing.T) {
	cases := map[string]struct {
		err    error
		reason string
	}{
		"too large":       {xcom.ErrTooLarge, "payload_too_large"},
		"schema mismatch": {xcom.ErrSchemaMismatch, "schema_mismatch"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv, a := newServerX(&fakeStore{spec: TaskSpec{Operator: "python"}}, &fakeXCom{pushErr: c.err})
			resp, err := srv.PushXCom(ctxWithToken(t, a), &agentv1.PushXComRequest{Value: []byte(`x`)})
			if err != nil {
				t.Fatalf("PushXCom returned a transport error: %v", err)
			}
			if resp.GetAccepted() || resp.GetRejectionReason() != c.reason {
				t.Errorf("rejection = (%v, %q), want (false, %q)", resp.GetAccepted(), resp.GetRejectionReason(), c.reason)
			}
		})
	}
}

func TestFetchXComReturnsDeclaredUpstream(t *testing.T) {
	x := &fakeXCom{entries: map[string]xcom.Entry{
		"xcom:acme:etl:run-1:upstream:return_value": {Value: []byte(`{"n":9}`), ContentType: "application/json"},
	}}
	store := &fakeStore{spec: TaskSpec{XComInputMapping: map[string]string{"val": "upstream"}}}
	srv, a := newServerX(store, x)

	resp, err := srv.FetchXCom(ctxWithToken(t, a), &agentv1.FetchXComRequest{UpstreamTaskId: "upstream"})
	if err != nil {
		t.Fatalf("FetchXCom: %v", err)
	}
	if string(resp.GetValue()) != `{"n":9}` {
		t.Errorf("value = %s", resp.GetValue())
	}
}

func TestFetchXComDeniesUndeclaredUpstream(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{XComInputMapping: map[string]string{"val": "other"}}}
	srv, a := newServerX(store, &fakeXCom{})
	_, err := srv.FetchXCom(ctxWithToken(t, a), &agentv1.FetchXComRequest{UpstreamTaskId: "secret"})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("fetching an undeclared upstream: code = %v, want PermissionDenied", status.Code(err))
	}
}

func TestFetchXComNotFound(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{XComInputMapping: map[string]string{"val": "upstream"}}}
	srv, a := newServerX(store, &fakeXCom{})
	_, err := srv.FetchXCom(ctxWithToken(t, a), &agentv1.FetchXComRequest{UpstreamTaskId: "upstream"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("missing xcom: code = %v, want NotFound", status.Code(err))
	}
}

type fakeLogWriter struct {
	lines  []string
	closed bool
}

func (w *fakeLogWriter) WriteEvent(ev logs.Event) error {
	w.lines = append(w.lines, ev.Message)
	return nil
}
func (w *fakeLogWriter) Close() error { w.closed = true; return nil }

func TestWriteLinesDrainsStreamToSink(t *testing.T) {
	msgs := []string{"line one", "line two", "line three"}
	i := 0
	recv := func() (*agentv1.LogLine, error) {
		if i >= len(msgs) {
			return nil, io.EOF
		}
		m := msgs[i]
		i++
		return &agentv1.LogLine{Message: m}, nil
	}
	w := &fakeLogWriter{}
	var published []string
	if err := writeLines(w, recv, func(l string) { published = append(published, l) }); err != nil {
		t.Fatalf("writeLines: %v", err)
	}
	if len(w.lines) != 3 || w.lines[2] != "line three" {
		t.Errorf("written lines = %v, want the three messages", w.lines)
	}
	// The tail channel now carries the full event JSON (level/stream/ts), so a
	// live NDJSON follower can color lines; decoding it yields the message.
	if len(published) != 3 || logs.DecodeLine(published[0]).Message != "line one" {
		t.Errorf("published lines = %v, want the three events tailed", published)
	}
}

func TestWriteLinesPropagatesRecvError(t *testing.T) {
	recv := func() (*agentv1.LogLine, error) { return nil, errors.New("stream broke") }
	if err := writeLines(&fakeLogWriter{}, recv, func(string) {}); err == nil {
		t.Error("a non-EOF receive error should propagate")
	}
}

// fakeStreamLogsServer is a minimal bidi server stream for StreamLogs tests.
type fakeStreamLogsServer struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*agentv1.LogLine
	i    int
}

func (s *fakeStreamLogsServer) Context() context.Context   { return s.ctx }
func (s *fakeStreamLogsServer) Send(*agentv1.LogAck) error { return nil }
func (s *fakeStreamLogsServer) Recv() (*agentv1.LogLine, error) {
	if s.i >= len(s.msgs) {
		return nil, io.EOF
	}
	m := s.msgs[s.i]
	s.i++
	return m, nil
}

func TestStreamLogsWritesToSink(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	sink := &fakeLogSink{}
	srv.SetLogSink(sink)

	stream := &fakeStreamLogsServer{
		ctx:  ctxWithToken(t, a),
		msgs: []*agentv1.LogLine{{Message: "hello"}, {Message: "world"}},
	}
	if err := srv.StreamLogs(stream); err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	if len(sink.lines) != 2 || sink.lines[0] != "hello" {
		t.Errorf("sink lines = %v, want the two messages", sink.lines)
	}
}

func TestStreamLogsUnimplementedWithoutSink(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	stream := &fakeStreamLogsServer{ctx: ctxWithToken(t, a)}
	if err := srv.StreamLogs(stream); status.Code(err) != codes.Unimplemented {
		t.Errorf("StreamLogs without a sink: code = %v, want Unimplemented", status.Code(err))
	}
}

func TestRPCsReportStoreErrors(t *testing.T) {
	errStore := &fakeStore{specErr: errors.New("db down")}
	srv, a := newServer(errStore)
	ctx := ctxWithToken(t, a)
	if _, err := srv.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{}); status.Code(err) != codes.Internal {
		t.Errorf("GetTaskSpec store error: code = %v, want Internal", status.Code(err))
	}
	if _, err := srv.FetchXCom(ctx, &agentv1.FetchXComRequest{UpstreamTaskId: "x"}); status.Code(err) != codes.Internal {
		t.Errorf("FetchXCom store error: code = %v, want Internal", status.Code(err))
	}

	repErr := &fakeStore{reportErr: errors.New("write failed")}
	srv2, a2 := newServer(repErr)
	if _, err := srv2.ReportState(ctxWithToken(t, a2), &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_SUCCESS}); status.Code(err) != codes.Internal {
		t.Errorf("ReportState store error: code = %v, want Internal", status.Code(err))
	}
}

type fakeLogSink struct{ lines []string }

func (s *fakeLogSink) Open(logs.Ref) (logs.LogWriter, error) { return &fakeSinkWriter{s: s}, nil }

type fakeSinkWriter struct{ s *fakeLogSink }

func (w *fakeSinkWriter) WriteEvent(ev logs.Event) error {
	w.s.lines = append(w.s.lines, ev.Message)
	return nil
}
func (w *fakeSinkWriter) Close() error { return nil }

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
