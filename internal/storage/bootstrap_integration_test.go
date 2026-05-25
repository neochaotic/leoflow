//go:build integration

package storage_test

import (
	"testing"

	"github.com/neochaotic/leoflow/internal/auth"
)

// TestBootstrapAdminHashIntegration checks the hash-only admin bootstrap used by
// Leoflow Lite: the stored hash must accept the password (login compatibility),
// and a second bootstrap must be a no-op once a user exists.
func TestBootstrapAdminHashIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	const email = "admin@leoflow.local"
	const pw = "river82"

	hash, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	created, err := repo.BootstrapAdminHash(ctx, "default", email, hash)
	if err != nil {
		t.Fatalf("BootstrapAdminHash: %v", err)
	}
	if created {
		// The hash setup persisted must verify the password the user wrote down.
		_, storedHash, ferr := repo.FindUserByLogin(ctx, "default", email)
		if ferr != nil {
			t.Fatalf("loading admin: %v", ferr)
		}
		if !auth.VerifyPassword(storedHash, pw) {
			t.Error("stored hash does not verify the bootstrap password")
		}
	}

	// Idempotent: bootstrapping again is a no-op while a user exists.
	again, err := repo.BootstrapAdminHash(ctx, "default", email, hash)
	if err != nil {
		t.Fatalf("second BootstrapAdminHash: %v", err)
	}
	if again {
		t.Error("second BootstrapAdminHash must be a no-op when users exist")
	}
}
