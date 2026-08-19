//go:build integration

package storage_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

// TestCreateAndFindOIDCUserIntegration exercises the OIDC provisioning and
// resolution queries end to end: a JIT-created OIDC-only user (no password) is
// linked by (provider, subject), granted the requested role, and resolved back
// by that immutable pair with its roles and permissions loaded and active=true.
func TestCreateAndFindOIDCUserIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("oidc_%d@example.com", time.Now().UnixNano())
	subject := fmt.Sprintf("sub-%d", time.Now().UnixNano())

	created, err := repo.CreateOIDCUser(ctx, "default", email, "azure", subject, []string{"viewer"})
	if err != nil {
		t.Fatalf("CreateOIDCUser: %v", err)
	}
	if created.ID == "" || created.Email != email || len(created.Roles) != 1 || created.Roles[0] != "viewer" {
		t.Fatalf("unexpected created oidc user: %+v", created)
	}

	got, active, err := repo.FindUserByOIDCSubject(ctx, "azure", subject)
	if err != nil {
		t.Fatalf("FindUserByOIDCSubject: %v", err)
	}
	if !active {
		t.Error("resolved oidc user is not active")
	}
	if got.ID != created.ID || got.Email != email || got.TenantID != "default" {
		t.Errorf("resolved user = %+v, want id=%s email=%s tenant=default", got, created.ID, email)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "viewer" {
		t.Errorf("roles = %v, want [viewer]", got.Roles)
	}
	// viewer carries the read:dag permission (PR-A ladder), proving perms load.
	if !got.HasPermission("read", "dag") {
		t.Errorf("resolved viewer lacks read:dag; perms = %v", got.Permissions)
	}
}

// TestFindOIDCUserNotFoundIntegration proves an unknown (provider, subject)
// pair yields auth.ErrUserNotFound — the signal the login flow uses to consider
// JIT provisioning rather than a hard failure.
func TestFindOIDCUserNotFoundIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	if _, _, err := repo.FindUserByOIDCSubject(ctx, "azure", "nobody-here"); !errors.Is(err, auth.ErrUserNotFound) {
		t.Errorf("err = %v, want auth.ErrUserNotFound", err)
	}
}

// TestCreateOIDCUserUnknownRoleIntegration proves an unknown role (e.g. a
// misconfigured default_role or role_mapping) fails closed as ErrValidation and
// leaves no orphaned account — the pair can be provisioned cleanly afterwards.
func TestCreateOIDCUserUnknownRoleIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("oidcbad_%d@example.com", time.Now().UnixNano())
	subject := fmt.Sprintf("subbad-%d", time.Now().UnixNano())

	if _, err := repo.CreateOIDCUser(ctx, "default", email, "azure", subject, []string{"wizard"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown role err = %v, want ErrValidation", err)
	}
	// No account was created, so a subsequent clean provision succeeds.
	if _, err := repo.CreateOIDCUser(ctx, "default", email, "azure", subject, nil); err != nil {
		t.Errorf("create after rejected role: %v", err)
	}
}

// TestCreateOIDCUserDuplicateSubjectIntegration proves a second provision of the
// same (provider, subject) surfaces as domain.ErrConflict (the unique
// constraint), so a concurrent double-login cannot create two identities.
func TestCreateOIDCUserDuplicateSubjectIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	subject := fmt.Sprintf("subdup-%d", time.Now().UnixNano())

	if _, err := repo.CreateOIDCUser(ctx, "default", fmt.Sprintf("a_%d@example.com", time.Now().UnixNano()), "azure", subject, nil); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if _, err := repo.CreateOIDCUser(ctx, "default", fmt.Sprintf("b_%d@example.com", time.Now().UnixNano()), "azure", subject, nil); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate subject err = %v, want ErrConflict", err)
	}
}

// TestRoleExistsIntegration proves the role existence check the OIDC login path
// uses to fail closed on a misconfigured default_role: a seeded role is found, a
// nonexistent one is not, and neither is an error.
func TestRoleExistsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	if ok, err := repo.RoleExists(ctx, "default", "viewer"); err != nil || !ok {
		t.Errorf("RoleExists(viewer) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := repo.RoleExists(ctx, "default", "wizard"); err != nil || ok {
		t.Errorf("RoleExists(wizard) = %v, %v; want false, nil", ok, err)
	}
}
