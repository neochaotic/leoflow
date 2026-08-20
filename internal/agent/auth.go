package agent

import (
	"context"
	"sync"
)

// TokenSource holds the agent's current bearer token behind a lock so the
// heartbeat loop can atomically swap it (token renewal, ADR 0055 Fix #4) while
// the gRPC per-RPC credential reads it on every outbound call. Reads and swaps
// may race across goroutines, so both go through the mutex.
type TokenSource struct {
	mu    sync.RWMutex
	token string
}

// NewTokenSource seeds a TokenSource with the dispatch token.
func NewTokenSource(token string) *TokenSource {
	return &TokenSource{token: token}
}

// Token returns the current bearer.
func (s *TokenSource) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// Set atomically swaps the bearer used by subsequent RPCs. An empty token is
// ignored so a "no renewal this beat" response never blanks a working
// credential.
func (s *TokenSource) Set(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	s.token = token
	s.mu.Unlock()
}

// tokenAuth is a gRPC per-RPC credential that attaches the agent's current
// bearer token to every call so the control plane can identify the task
// instance. It reads the live token from the shared TokenSource on each call, so
// a renewal swap takes effect on the very next RPC.
type tokenAuth struct {
	source *TokenSource
	secure bool
}

// GetRequestMetadata returns the authorization header carrying the current
// bearer token.
func (t tokenAuth) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + t.source.Token()}, nil
}

// RequireTransportSecurity reports whether the credential may only travel over a
// secure transport. It is false in local development against an insecure cluster.
func (t tokenAuth) RequireTransportSecurity() bool { return t.secure }
