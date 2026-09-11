package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// #843 established the rule for IssueToken: a backend (DB) error must PROPAGATE
// rather than be reported as a credential failure, because a database outage
// reported as "wrong password" masks the incident. Authenticate — the sibling
// that validates an already-issued token — did the opposite: it joined
// ErrInvalidToken onto EVERY store error, so a dead database and a forged token
// were the same value to every caller.
//
// Measured against a real stopped Postgres during the v0.4.6 validation: the API
// answered 401 "invalid token" after 76 seconds, on a token minted seconds
// earlier (#1087). Signature verification is local and takes about a
// millisecond; the 76 seconds were the store timing out.
func TestAuthenticatePropagatesBackendError(t *testing.T) {
	dbErr := errors.New("dial tcp 10.0.0.1:5432: i/o timeout")
	// Mint with a healthy store, validate against a dead one, same secret — the
	// shape of a control plane whose database fails after a user logged in.
	minter := NewJWTAuthenticator(
		&fakeStore{user: &User{ID: "u1", TenantID: "default"}, hash: must(HashPassword("pw"))},
		"secret", time.Hour)
	tok, err := minter.IssueToken(context.Background(), Credentials{Tenant: "default", Username: "a@b.c", Password: "pw"})
	if err != nil {
		t.Fatalf("minting a token: %v", err)
	}

	down := NewJWTAuthenticator(&fakeStore{byIDErr: dbErr}, "secret", time.Hour)
	if _, aerr := down.Authenticate(context.Background(), tok); !errors.Is(aerr, dbErr) {
		t.Errorf("a backend error must propagate unchanged; got %v", aerr)
	} else if errors.Is(aerr, ErrInvalidToken) {
		t.Error("a backend error must NOT be reported as an invalid token — that conflation is #1087")
	}
}

// The fail-closed behavior the conflation was protecting must survive: a token
// whose subject has no user row is still invalid, so deleting a user revokes
// immediately instead of leaving claimed roles alive until the token expires.
func TestAuthenticateKeepsFailClosedOnMissingUser(t *testing.T) {
	minter := NewJWTAuthenticator(
		&fakeStore{user: &User{ID: "u1", TenantID: "default"}, hash: must(HashPassword("pw"))},
		"secret", time.Hour)
	tok, err := minter.IssueToken(context.Background(), Credentials{Tenant: "default", Username: "a@b.c", Password: "pw"})
	if err != nil {
		t.Fatalf("minting a token: %v", err)
	}

	gone := NewJWTAuthenticator(&fakeStore{byIDErr: ErrUserNotFound}, "secret", time.Hour)
	if _, aerr := gone.Authenticate(context.Background(), tok); !errors.Is(aerr, ErrInvalidToken) {
		t.Errorf("a deleted user must still be ErrInvalidToken (fail closed); got %v", aerr)
	}
}

// A malformed token never reaches the store, so it must stay invalid even when
// the store is down — otherwise an outage would turn every forged token into a
// 503 and hide the rejection.
func TestAuthenticateMalformedTokenStaysInvalidDuringAnOutage(t *testing.T) {
	down := NewJWTAuthenticator(&fakeStore{byIDErr: errors.New("dial tcp: i/o timeout")}, "secret", time.Hour)
	_, aerr := down.Authenticate(context.Background(), "not-a-jwt")
	if !errors.Is(aerr, ErrInvalidToken) {
		t.Errorf("a malformed token must be ErrInvalidToken; got %v", aerr)
	}
}
