package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/neochaotic/leoflow/internal/config"
)

// TestDiscoverOIDCFlowDiscoveryFailureIsBootError locks that an unreachable
// issuer fails boot closed rather than deferring the failure to first login —
// the fail-closed posture the OIDC flow depends on.
func TestDiscoverOIDCFlowDiscoveryFailureIsBootError(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Auth.Provider = config.AuthProviderOIDC
	cfg.Auth.JWT.Secret = "secret"
	// A syntactically valid but unreachable issuer: discovery must fail.
	cfg.Auth.OIDC.Issuer = "https://127.0.0.1:1/does-not-exist"
	cfg.Auth.OIDC.ClientID = "client"
	cfg.Auth.OIDC.RedirectURL = "https://app.example/api/v2/auth/oidc/callback"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, err := discoverOIDCFlow(context.Background(), cfg, logger); err == nil {
		t.Error("discoverOIDCFlow with an unreachable issuer = nil error, want a boot failure")
	}
}
