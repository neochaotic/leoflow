package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/domain"
)

type fakeNameChecker struct {
	tenants map[string]bool
	roles   map[string]bool // "tenant/role"
	err     error
}

func (f fakeNameChecker) TenantExists(_ context.Context, name string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.tenants[name], nil
}

func (f fakeNameChecker) RoleExists(_ context.Context, tenant, role string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if !f.tenants[tenant] {
		return false, domain.ErrNotFound
	}
	return f.roles[tenant+"/"+role], nil
}

func oidcAuthFor(mut func(*config.AuthSection)) config.AuthSection {
	c := config.AuthSection{Provider: config.AuthProviderOIDC}
	mut(&c)
	return c
}

// TestNameWarningsCatchAConfigurationNothingCanSatisfy covers the last shape in
// this family: names that are syntactically fine, pass every config gate, and
// refer to rows that do not exist.
//
// Nothing in this project creates a tenant. INSERT INTO tenants appears exactly
// once, in migration 001, creating "default"; no API, CLI, chart setting or
// later migration adds another. So a tenant_claims entry mapping a claim value
// to any other name boots green and denies every login carrying it, behind the
// same generic 403 as everything else. The role ladder is seeded for "default"
// alone, so a typo in default_role does the same.
//
// Both are checkable at boot: Postgres is already connected and its schema
// verified before the API starts.
func TestNameWarningsCatchAConfigurationNothingCanSatisfy(t *testing.T) {
	ck := fakeNameChecker{
		tenants: map[string]bool{"default": true},
		roles:   map[string]bool{"default/viewer": true, "default/editor": true},
	}

	t.Run("a tenant that does not exist", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "acme"}
		}))
		if len(w) == 0 {
			t.Fatal("no warning: every login carrying that claim value is denied, and nothing in this project can create the tenant")
		}
		if !strings.Contains(w[0].Msg, "acme") || !strings.Contains(w[0].Msg, "corp.example") {
			t.Errorf("the warning names neither the tenant nor the claim value it is mapped from: %s", w[0].Msg)
		}
	})

	t.Run("a default_role that does not exist", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
			a.OIDC.DefaultRole = "viewr"
		}))
		if len(w) == 0 {
			t.Fatal("no warning: a typo in the role this project's own remedy tells operators to set denies every login")
		}
		if !strings.Contains(w[0].Msg, "viewr") {
			t.Errorf("the warning does not quote the name that is wrong: %s", w[0].Msg)
		}
	})

	t.Run("a role_mappings value that does not exist", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
			a.OIDC.RoleMappings = map[string]string{"data-eng": "edtior"}
		}))
		if len(w) == 0 {
			t.Fatal("no warning: a user in that group is denied, and only that group")
		}
		if !strings.Contains(w[0].Msg, "edtior") {
			t.Errorf("the warning does not quote the name that is wrong: %s", w[0].Msg)
		}
	})

	t.Run("everything exists: silent", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default", "b.example": "default"}
			a.OIDC.DefaultRole = "viewer"
			a.OIDC.RoleMappings = map[string]string{"data-eng": "editor"}
		}))
		if len(w) != 0 {
			t.Errorf("warned about a configuration whose every name resolves: %s", w[0].Msg)
		}
	})

	t.Run("not oidc: silent, and no query is made", func(t *testing.T) {
		exploding := fakeNameChecker{err: errors.New("must not be called")}
		if len(oidcNameWarnings(t.Context(), exploding, config.AuthSection{Provider: config.AuthProviderJWT})) != 0 {
			t.Error("warned on a jwt deployment")
		}
	})

	t.Run("a failed query says so instead of passing quietly", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), fakeNameChecker{err: errors.New("connection reset")}, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
		}))
		if len(w) == 0 {
			t.Fatal("a check that could not run reported nothing, which is indistinguishable from a check that found nothing")
		}
		if !strings.Contains(w[0].Msg, "could not") {
			t.Errorf("the warning does not say the check failed to run: %s", w[0].Msg)
		}
	})

	t.Run("roles are checked once per tenant, not once per claim value", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"a.example": "default", "b.example": "default", "c.example": "default"}
			a.OIDC.DefaultRole = "viewr"
		}))
		if len(w) != 1 {
			t.Errorf("got %d warnings for one missing role mapped from three domains; a repeated warning is a warning operators stop reading", len(w))
		}
	})
}

// TestBreakGlassWarningNamesTheLockOut covers the setting that decides whether any
// of the other failures is recoverable.
//
// With provider: oidc and an empty allowlist, newBreakGlass admits nobody: every
// password login is rejected. That is correct and it is also the state in which
// a wrong tenant pin, an IdP outage or any of the other denials leaves NOBODY
// able to reach the control plane, including the operator who has to fix it. The
// chart's own example calls it the lock-out escape hatch and nothing warned.
func TestBreakGlassWarningNamesTheLockOut(t *testing.T) {
	t.Run("empty under oidc: warns", func(t *testing.T) {
		w := oidcBreakGlassWarnings(oidcAuthFor(func(*config.AuthSection) {}))
		if len(w) == 0 {
			t.Fatal("no warning: an IdP problem locks every operator out with no way back in")
		}
		if !strings.Contains(w[0].Msg, "break_glass_emails") {
			t.Errorf("the warning does not name the setting: %s", w[0].Msg)
		}
	})

	t.Run("configured: silent", func(t *testing.T) {
		w := oidcBreakGlassWarnings(oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.BreakGlassEmails = []string{"admin@corp.example"}
		}))
		if len(w) != 0 {
			t.Errorf("warned on a deployment that has a way back in: %s", w[0].Msg)
		}
	})

	t.Run("not oidc: silent", func(t *testing.T) {
		if len(oidcBreakGlassWarnings(config.AuthSection{Provider: config.AuthProviderJWT})) != 0 {
			t.Error("warned on a jwt deployment, where the credential path is the primary login")
		}
	})
}

// TestWarnOIDCNamesReachesTheProcessThatServesLogins pins the wiring. A check
// that runs and logs nowhere is not a check, and the scheduler role never serves
// a login, so gating it there would hide it from the only process the missing
// names break.
func TestWarnOIDCNamesReachesTheProcessThatServesLogins(t *testing.T) {
	ck := fakeNameChecker{tenants: map[string]bool{"default": true}}
	cfg := &config.ServerConfig{}
	cfg.Auth.Provider = config.AuthProviderOIDC
	cfg.Auth.OIDC.TenantClaims = map[string]string{"corp.example": "acme"}

	for _, tc := range []struct {
		name string
		role string
		want bool
	}{
		{"api-only replica logs it", config.RoleAPI, true},
		{"scheduler-only replica stays quiet", config.RoleScheduler, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.Server.Role = tc.role
			var buf bytes.Buffer
			warnOIDCNames(t.Context(), ck, cfg, slog.New(slog.NewTextHandler(&buf, nil)))
			if got := strings.Contains(buf.String(), "acme"); got != tc.want {
				t.Errorf("logged=%v, want %v:\n%s", got, tc.want, buf.String())
			}
		})
	}
}

// TestWarnOIDCNamesDoesNotHangBootOnASlowDatabase locks the bound. This runs
// before the HTTP listener binds, so a query that never returns means the probe
// endpoint never comes up and the kubelet restarts the pod on a probe failure
// that names nothing, which is the failure mode the OIDC discovery bound exists
// to prevent one step earlier.
func TestWarnOIDCNamesDoesNotHangBootOnASlowDatabase(t *testing.T) {
	restore := oidcNameCheckTimeout
	oidcNameCheckTimeout = 100 * time.Millisecond
	t.Cleanup(func() { oidcNameCheckTimeout = restore })

	cfg := &config.ServerConfig{}
	cfg.Auth.Provider = config.AuthProviderOIDC
	cfg.Auth.OIDC.TenantClaims = map[string]string{"corp.example": "default"}

	done := make(chan struct{})
	go func() {
		warnOIDCNames(t.Context(), blockingNameChecker{}, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("boot is unbounded against a database that accepts the query and never answers, so the listener never binds")
	}
}

// blockingNameChecker answers only when the context is done, which is what a
// query against a wedged database looks like from here.
type blockingNameChecker struct{}

func (blockingNameChecker) TenantExists(ctx context.Context, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func (blockingNameChecker) RoleExists(ctx context.Context, _, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}
