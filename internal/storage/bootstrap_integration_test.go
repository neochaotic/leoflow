//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
)

// TestPasswordRecoveryLoginIntegration is the end-to-end recovery flow behind
// `leoflow lite reset-password`: after a reset, the admin must be able to LOG IN
// with the new password (issue a token), and the old password must stop working.
// This guards the real recovery scenario, not just the DB hash update.
func TestPasswordRecoveryLoginIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	const email = "admin@leoflow.local"
	const oldPW, newPW = "old-secret-1", "new-secret-2"

	oldHash, err := auth.HashPassword(oldPW)
	if err != nil {
		t.Fatal(err)
	}
	// Ensure the admin exists (creates it on a fresh tenant; a no-op otherwise),
	// then force a known starting password — robust whether or not the DB already
	// has an admin (BootstrapAdminHash only seeds an empty tenant).
	if _, err := repo.BootstrapAdminHash(ctx, "default", email, oldHash); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if ok, err := repo.SetUserPassword(ctx, "default", email, oldHash); err != nil || !ok {
		t.Fatalf("seed starting password: ok=%v err=%v", ok, err)
	}

	authn := auth.NewJWTAuthenticator(repo, "recovery-test-secret", time.Hour)
	login := func(pw string) error {
		_, e := authn.IssueToken(context.Background(), auth.Credentials{Tenant: "default", Username: email, Password: pw})
		return e
	}

	// Sanity: the original password logs in.
	if err := login(oldPW); err != nil {
		t.Fatalf("login with original password failed: %v", err)
	}

	// Recover: reset to a new password (what reset-password does).
	newHash, err := auth.HashPassword(newPW)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.SetUserPassword(ctx, "default", email, newHash); err != nil || !ok {
		t.Fatalf("reset password: ok=%v err=%v", ok, err)
	}

	// The new password now logs in; the old one no longer does.
	if err := login(newPW); err != nil {
		t.Errorf("login with the reset password failed (recovery broken): %v", err)
	}
	if err := login(oldPW); err == nil {
		t.Error("old password still logs in after reset")
	}
}

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

// TestSetUserPasswordIntegration checks the reset-password DB update: the new
// hash must replace the old one (and verify the new password), and an unknown
// user reports no update.
func TestSetUserPasswordIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	const email = "admin@leoflow.local"

	old, err := auth.HashPassword("oldpass1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BootstrapAdminHash(ctx, "default", email, old); err != nil {
		t.Fatalf("ensure admin: %v", err)
	}

	newHash, err := auth.HashPassword("river99")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := repo.SetUserPassword(ctx, "default", email, newHash)
	if err != nil {
		t.Fatalf("SetUserPassword: %v", err)
	}
	if !ok {
		t.Fatal("SetUserPassword returned false for an existing admin")
	}
	_, stored, ferr := repo.FindUserByLogin(ctx, "default", email)
	if ferr != nil {
		t.Fatalf("load admin: %v", ferr)
	}
	if !auth.VerifyPassword(stored, "river99") {
		t.Error("reset hash does not verify the new password")
	}
	if auth.VerifyPassword(stored, "oldpass1") {
		t.Error("old password still verifies after reset")
	}

	// Unknown user: no update.
	got, err := repo.SetUserPassword(ctx, "default", "nobody@example.com", newHash)
	if err != nil {
		t.Fatalf("SetUserPassword(unknown): %v", err)
	}
	if got {
		t.Error("SetUserPassword reported an update for a nonexistent user")
	}
}
