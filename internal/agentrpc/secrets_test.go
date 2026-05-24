package agentrpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

type fakeSecrets struct {
	vars     map[string]string
	conns    map[string]string
	gotVarTn string
}

func (f *fakeSecrets) SecretVariables(_ context.Context, tenant string) (map[string]string, error) {
	f.gotVarTn = tenant
	return f.vars, nil
}
func (f *fakeSecrets) SecretConnectionURIs(_ context.Context, _ string) (map[string]string, error) {
	return f.conns, nil
}

func TestGetVariablesAndConnections(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	sec := &fakeSecrets{
		vars:  map[string]string{"FOO": "bar"},
		conns: map[string]string{"pg": "postgres://u:p@h:5432/db"},
	}
	srv.SetSecrets(sec, true) // dev: allow over the insecure test channel
	ctx := ctxWithToken(t, a)

	vresp, err := srv.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if vresp.Variables["FOO"] != "bar" {
		t.Errorf("variables = %v", vresp.Variables)
	}
	if sec.gotVarTn != "acme" { // scoped to the token's tenant
		t.Errorf("tenant = %q, want acme", sec.gotVarTn)
	}
	cresp, err := srv.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
	if err != nil {
		t.Fatalf("GetConnections: %v", err)
	}
	if cresp.ConnectionUris["pg"] != "postgres://u:p@h:5432/db" {
		t.Errorf("connections = %v", cresp.ConnectionUris)
	}
}

func TestSecretsFailClosedOnInsecureChannel(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	srv.SetSecrets(&fakeSecrets{vars: map[string]string{"X": "1"}}, false) // require TLS
	ctx := ctxWithToken(t, a)                                              // no TLS peer in the test context

	if _, err := srv.GetVariables(ctx, &agentv1.GetVariablesRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("GetVariables over insecure channel = %v, want PermissionDenied", err)
	}
	if _, err := srv.GetConnections(ctx, &agentv1.GetConnectionsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("GetConnections over insecure channel = %v, want PermissionDenied", err)
	}
}

func TestSecretsRejectMissingToken(t *testing.T) {
	srv, _ := newServer(&fakeStore{})
	srv.SetSecrets(&fakeSecrets{}, true)
	if _, err := srv.GetVariables(context.Background(), &agentv1.GetVariablesRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("missing token = %v, want Unauthenticated", err)
	}
}
