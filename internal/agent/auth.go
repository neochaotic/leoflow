package agent

import "context"

// tokenAuth is a gRPC per-RPC credential that attaches the agent's bearer token
// to every call so the control plane can identify the task instance.
type tokenAuth struct {
	token  string
	secure bool
}

// GetRequestMetadata returns the authorization header carrying the bearer token.
func (t tokenAuth) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + t.token}, nil
}

// RequireTransportSecurity reports whether the credential may only travel over a
// secure transport. It is false in local development against an insecure cluster.
func (t tokenAuth) RequireTransportSecurity() bool { return t.secure }
