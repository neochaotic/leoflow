package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A backend (DB) error from the store must PROPAGATE, not be reported as invalid
// credentials — otherwise a database outage looks like "wrong password" to every
// user and masks the incident (#843).
func TestIssueTokenPropagatesBackendError(t *testing.T) {
	dbErr := errors.New("dial tcp 127.0.0.1:5432: connection refused")
	a := NewJWTAuthenticator(&fakeStore{err: dbErr}, "secret", time.Hour)
	_, err := a.IssueToken(context.Background(), Credentials{Tenant: "default", Username: "a@b.c", Password: "pw"})
	if !errors.Is(err, dbErr) {
		t.Errorf("a backend error must propagate unchanged; got %v", err)
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Error("a backend error must NOT be reported as invalid credentials")
	}
}

// A genuine not-found / inactive user (the store returns ErrInvalidCredentials)
// stays ErrInvalidCredentials — the generic, no-enumeration answer.
func TestIssueTokenKeepsInvalidCredentials(t *testing.T) {
	a := NewJWTAuthenticator(&fakeStore{err: ErrInvalidCredentials}, "secret", time.Hour)
	if _, err := a.IssueToken(context.Background(), Credentials{Tenant: "default", Username: "x", Password: "pw"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a store not-found/inactive must stay ErrInvalidCredentials; got %v", err)
	}
}

// A wrong password is ErrInvalidCredentials.
func TestIssueTokenBadPassword(t *testing.T) {
	a := NewJWTAuthenticator(
		&fakeStore{user: &User{ID: "u1", TenantID: "default"}, hash: must(HashPassword("right"))},
		"secret", time.Hour)
	if _, err := a.IssueToken(context.Background(), Credentials{Tenant: "default", Username: "a@b.c", Password: "wrong"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a wrong password must be ErrInvalidCredentials; got %v", err)
	}
}
