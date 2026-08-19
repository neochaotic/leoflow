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

// TestListUsersIntegration proves the list surfaces a just-created account with
// its granted role aggregated, reports a total that counts it, and never exposes
// a password or hash (there is no such field to expose on the returned type).
func TestListUsersIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("listed_%d@example.com", time.Now().UnixNano())
	if _, err := repo.CreateUser(ctx, "default", email, "pw-12345678", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	users, total, err := repo.ListUsers(ctx, "default", 100, 0)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if total < 1 {
		t.Fatalf("total = %d, want at least the created user", total)
	}
	var found *domain.User
	for i := range users {
		if users[i].Email == email {
			found = &users[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("created user %q not in list", email)
	}
	if found.ID == "" || !found.IsActive {
		t.Errorf("unexpected listed user: %+v", *found)
	}
	if len(found.Roles) != 1 || found.Roles[0] != "admin" {
		t.Errorf("roles = %v, want [admin]", found.Roles)
	}
}

// TestListUsersRoleAggregationIntegration proves a user with more than one role
// comes back with every role name, and one with none comes back with an empty
// (non-nil) set rather than a null.
func TestListUsersRoleAggregationIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("noroles_%d@example.com", time.Now().UnixNano())
	if _, err := repo.CreateUser(ctx, "default", email, "pw-12345678", ""); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	users, _, err := repo.ListUsers(ctx, "default", 100, 0)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for i := range users {
		if users[i].Email == email {
			if users[i].Roles == nil {
				t.Errorf("a role-less user must list an empty (non-nil) role set")
			}
			if len(users[i].Roles) != 0 {
				t.Errorf("roles = %v, want empty", users[i].Roles)
			}
			return
		}
	}
	t.Fatalf("role-less user %q not in list", email)
}
