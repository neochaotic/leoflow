package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestDiscoverOIDCFlowIsBoundedWhenTheIdPHangs covers the third shape the SSO
// audit found (#1153): an IdP that accepts the TCP connection and then never
// answers.
//
// Discovery runs before the HTTP listener is bound, and go-oidc issues it on a
// client with no timeout of its own, so a hung issuer parks boot forever. On
// Kubernetes the probe endpoint never comes up, the kubelet kills the container,
// and the deployment enters a restart loop whose events say only that the probe
// failed. Nothing anywhere names the IdP. Unreachable already failed fast (the
// connection is refused); hung is the case with no bound.
//
// A bound turns it into a named boot failure. It stays fail-closed either way:
// the server does not start with a flow that cannot verify anything.
func TestDiscoverOIDCFlowIsBoundedWhenTheIdPHangs(t *testing.T) {
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-blocked
	}))
	t.Cleanup(func() { close(blocked); srv.Close() })

	restore := oidcDiscoveryTimeout
	oidcDiscoveryTimeout = 150 * time.Millisecond
	t.Cleanup(func() { oidcDiscoveryTimeout = restore })

	cfg := &config.ServerConfig{}
	cfg.Auth.Provider = config.AuthProviderOIDC
	cfg.Auth.JWT.Secret = "secret"
	cfg.Auth.OIDC.Issuer = srv.URL
	cfg.Auth.OIDC.ClientID = "client"
	cfg.Auth.OIDC.RedirectURL = "https://app.example/api/v2/auth/oidc/callback"

	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		_, err := discoverOIDCFlow(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		done <- result{err}
	}()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("a hung issuer produced a usable flow")
		}
		for _, want := range []string{srv.URL, "did not answer"} {
			if !strings.Contains(got.err.Error(), want) {
				t.Errorf("the boot error does not contain %q, so the operator cannot tell the IdP hung: %v", want, got.err)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("discovery never returned: boot is unbounded against an IdP that accepts the connection and never answers, so the listener never binds and the kubelet restarts the pod forever")
	}
}
