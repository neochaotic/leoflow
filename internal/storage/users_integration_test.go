//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

// TestCreateUserIntegration exercises the admin create-user query end to end:
// the new user is persisted, granted the requested role, and can authenticate
// with the supplied password (proving the reused bcrypt hashing round-trips).
func TestCreateUserIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("newuser_%d@example.com", time.Now().UnixNano())
	const password = "create-user-secret-1"

	got, err := repo.CreateUser(ctx, "default", email, password, "admin")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if got.ID == "" || got.Email != email || got.Role != "admin" || !got.IsActive {
		t.Fatalf("unexpected created user: %+v", got)
	}

	// The created user can log in with the plaintext password (hash round-trips),
	// and inherits the admin role's permissions.
	authn := auth.NewJWTAuthenticator(repo, "create-user-test-secret", time.Hour)
	if _, lerr := authn.IssueToken(ctx, auth.Credentials{Tenant: "default", Username: email, Password: password}); lerr != nil {
		t.Errorf("created user cannot log in: %v", lerr)
	}
}

// TestCreateUserDuplicateEmailIntegration proves the unique-constraint violation
// surfaces as domain.ErrConflict (the API maps it to 409), not a raw 500.
func TestCreateUserDuplicateEmailIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("dupe_%d@example.com", time.Now().UnixNano())

	if _, err := repo.CreateUser(ctx, "default", email, "pw-first-1234", "admin"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := repo.CreateUser(ctx, "default", email, "pw-second-1234", "admin"); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate create err = %v, want ErrConflict", err)
	}
}

// TestCreateUserUnknownRoleIntegration proves an unknown role is rejected as a
// validation error and leaves no orphaned account behind (the role is resolved
// before the user is inserted).
func TestCreateUserUnknownRoleIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("norole_%d@example.com", time.Now().UnixNano())

	if _, err := repo.CreateUser(ctx, "default", email, "pw-12345678", "wizard"); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown role err = %v, want ErrValidation", err)
	}
	// No account was created, so a subsequent create with a valid (empty) role
	// succeeds rather than colliding.
	if _, err := repo.CreateUser(ctx, "default", email, "pw-12345678", ""); err != nil {
		t.Errorf("create after rejected role: %v", err)
	}
}
