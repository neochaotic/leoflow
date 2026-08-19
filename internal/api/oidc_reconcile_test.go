package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/neochaotic/leoflow/internal/auth"
)

// TestOIDCReturningUserReconcilesToMappedRoles proves the IdP is authoritative
// for a returning user: a user previously granted [operator] whose IdP groups now
// map to [viewer] has the DB roles reconciled to exactly [viewer] on login, and
// the minted token carries the same set. The demotion therefore takes effect for
// the per-request authz reload, not just the token.
func TestOIDCReturningUserReconcilesToMappedRoles(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.RoleMappings = map[string]string{"data-eng": "viewer"} // group now maps to viewer
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"operator"}}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	rec := driveCallback(t, srv, f, cfg, nil) // groups: [data-eng]

	if rec.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302", rec.Code)
	}
	roles, ok := store.lastReconcile("user-1")
	if !ok {
		t.Fatal("returning user was not reconciled")
	}
	if len(roles) != 1 || roles[0] != "viewer" {
		t.Errorf("reconciled roles = %v, want [viewer] (IdP-authoritative demotion)", roles)
	}
	if ck := sessionCookie(rec); ck == nil {
		t.Fatal("no session cookie")
	} else if tr := tokenRoles(t, ck.Value); len(tr) != 1 || tr[0] != "viewer" {
		t.Errorf("token roles = %v, want [viewer]", tr)
	}
}

// TestOIDCReturningUserEmptyMappingWithDefaultRole proves a returning user whose
// groups map to no role is reconciled to [default_role] when one is set.
func TestOIDCReturningUserEmptyMappingWithDefaultRole(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.DefaultRole = "viewer"
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"operator"}}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	rec := driveCallback(t, srv, f, cfg, func(c jwt.MapClaims) { c["groups"] = []string{"unmapped-group"} })

	if rec.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302", rec.Code)
	}
	roles, ok := store.lastReconcile("user-1")
	if !ok || len(roles) != 1 || roles[0] != "viewer" {
		t.Errorf("reconciled roles = %v (found=%v), want [viewer] via default_role", roles, ok)
	}
}

// TestOIDCReturningUserEmptyMappingNoDefaultRole proves a returning user whose
// groups map to no role and with no default_role is reconciled to the empty set
// (default-deny) — a prior grant is stripped.
func TestOIDCReturningUserEmptyMappingNoDefaultRole(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.DefaultRole = ""
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"operator"}}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	rec := driveCallback(t, srv, f, cfg, func(c jwt.MapClaims) { c["groups"] = []string{"unmapped-group"} })

	if rec.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302", rec.Code)
	}
	roles, ok := store.lastReconcile("user-1")
	if !ok {
		t.Fatal("returning user was not reconciled")
	}
	if len(roles) != 0 {
		t.Errorf("reconciled roles = %v, want [] (default-deny)", roles)
	}
}

// TestOIDCJITUserAlsoReconciles proves reconciliation runs for the JIT path too,
// with the same mapped set the account was created with — so both paths converge
// on identical DB state.
func TestOIDCJITUserAlsoReconciles(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.JITProvisioning = true
	store := newFakeOIDCStore()
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	rec := driveCallback(t, srv, f, cfg, nil) // groups [data-eng] → editor

	if rec.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302", rec.Code)
	}
	if len(store.created) != 1 {
		t.Fatalf("JIT created %d users, want 1", len(store.created))
	}
	roles, ok := store.lastReconcile("jit-subject-123")
	if !ok {
		t.Fatal("JIT-created user was not reconciled")
	}
	if len(roles) != 1 || roles[0] != "editor" {
		t.Errorf("reconciled roles = %v, want [editor]", roles)
	}
}

// TestOIDCReconcileFailureFailsClosed proves a reconcile error rejects the login:
// no session cookie is minted and the failure is audited, so a role write that
// cannot be applied never yields a token whose roles the DB does not back.
func TestOIDCReconcileFailureFailsClosed(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"editor"}}, true)
	store.reconcileErr = errors.New("db unavailable")
	audit := &fakeAuthAudit{}
	srv := oidcServer(t, f, cfg, store, audit, nil)

	rec := driveCallback(t, srv, f, cfg, nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("reconcile failure = %d, want 403 (fail closed)", rec.Code)
	}
	if sessionCookie(rec) != nil {
		t.Error("a failed reconcile must NOT mint a session cookie")
	}
	if !audit.has(auditOIDCLoginFailure, "denied") {
		t.Error("the reconcile failure was not audited")
	}
}
