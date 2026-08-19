package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/neochaotic/leoflow/internal/config"
)

// TestBuildOIDCFlowNilInJWTMode locks that the default (provider: jwt) never
// attempts IdP discovery and returns a nil flow, so the OIDC routes stay
// unregistered and boot does no network I/O for auth.
func TestBuildOIDCFlowNilInJWTMode(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Auth.Provider = config.AuthProviderJWT
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	flow, err := buildOIDCFlow(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("buildOIDCFlow(jwt) = %v, want nil error", err)
	}
	if flow != nil {
		t.Error("buildOIDCFlow(jwt) returned a non-nil flow; jwt mode must not build one")
	}
}

// TestBuildOIDCFlowDiscoveryFailureIsBootError locks that an unreachable issuer
// fails boot closed rather than deferring the failure to first login.
func TestBuildOIDCFlowDiscoveryFailureIsBootError(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Auth.Provider = config.AuthProviderOIDC
	cfg.Auth.JWT.Secret = "secret"
	// A syntactically valid but unreachable issuer: discovery must fail.
	cfg.Auth.OIDC.Issuer = "https://127.0.0.1:1/does-not-exist"
	cfg.Auth.OIDC.ClientID = "client"
	cfg.Auth.OIDC.RedirectURL = "https://app.example/api/v2/auth/oidc/callback"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, err := buildOIDCFlow(context.Background(), cfg, logger); err == nil {
		t.Error("buildOIDCFlow with an unreachable issuer = nil error, want a boot failure")
	}
}
