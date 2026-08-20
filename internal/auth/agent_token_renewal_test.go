package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// originOf decodes the preserved dispatch-origin claim (oiat) from a signed agent
// token, so a test can assert renewal never advances it.
func originOf(t *testing.T, a *JWTAuthenticator, token string) time.Time {
	t.Helper()
	var c agentClaims
	if _, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceAgent), jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(a.clock)); err != nil {
		t.Fatalf("parsing renewed token: %v", err)
	}
	if c.OriginIssuedAt == nil {
		t.Fatalf("renewed token carries no origin (oiat) claim")
	}
	return c.OriginIssuedAt.Time
}

func iatExpOf(t *testing.T, a *JWTAuthenticator, token string) (iat, exp time.Time) {
	t.Helper()
	var c agentClaims
	if _, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceAgent), jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(a.clock)); err != nil {
		t.Fatalf("parsing token: %v", err)
	}
	return c.IssuedAt.Time, c.ExpiresAt.Time
}

// TestRenewAgentTokenShortTTLAndPreservedOrigin: a renewal for a live attempt
// re-mints the SAME identity with a fresh short TTL (exp = now + ttl, never
// accumulating) while preserving the original dispatch time so the attempt's
// total credential lifetime can be bounded across renewals.
func TestRenewAgentTokenShortTTLAndPreservedOrigin(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0 }

	// Dispatch mints the initial per-attempt token; its origin is t0.
	dispatched, err := a.IssueAgentToken(agentIdentity(), 10*time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if origin := originOf(t, a, dispatched); !origin.Equal(t0) {
		t.Fatalf("dispatch origin = %v, want %v", origin, t0)
	}

	// Five minutes later, the attempt heartbeats and renews.
	t1 := t0.Add(5 * time.Minute)
	a.now = func() time.Time { return t1 }
	renewed, ok, err := a.RenewAgentToken(dispatched, 10*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("RenewAgentToken: %v", err)
	}
	if !ok {
		t.Fatal("renewal within the ceiling must succeed (ok=false)")
	}

	// The renewed token verifies unchanged (signature/issuer/audience/method) and
	// carries the same identity.
	got, err := a.AuthenticateAgent(renewed)
	if err != nil {
		t.Fatalf("AuthenticateAgent(renewed): %v", err)
	}
	if *got != agentIdentity() {
		t.Errorf("renewed identity = %+v, want %+v", *got, agentIdentity())
	}

	// exp = now + ttl exactly (never accumulated onto the previous exp).
	iat, exp := iatExpOf(t, a, renewed)
	if !iat.Equal(t1) {
		t.Errorf("renewed iat = %v, want %v (the renewal instant)", iat, t1)
	}
	if exp.Sub(iat) != 10*time.Minute {
		t.Errorf("renewed exp-iat = %v, want %v (fresh short TTL, no accumulation)", exp.Sub(iat), 10*time.Minute)
	}

	// The origin is preserved across the renewal — it still points at dispatch.
	if origin := originOf(t, a, renewed); !origin.Equal(t0) {
		t.Errorf("renewed origin = %v, want the dispatch time %v (must not advance)", origin, t0)
	}
}

// TestRenewAgentTokenCeilingStopsRenewal: once an attempt has been alive past the
// max-lifetime ceiling since first dispatch, renewal is refused (ok=false, no
// token) so a runaway attempt's credential lapses rather than living forever.
func TestRenewAgentTokenCeilingStopsRenewal(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	// Simulate a token that has been renewed for 25h: it is freshly minted (so
	// still valid) but its preserved origin still points at the original dispatch
	// at t0. This is exactly the shape RenewAgentToken re-mints on every beat.
	a.now = func() time.Time { return t0.Add(25 * time.Hour) }
	aged, err := a.mintAgentToken(agentIdentity(), 10*time.Minute, t0)
	if err != nil {
		t.Fatalf("mintAgentToken: %v", err)
	}
	renewed, ok, err := a.RenewAgentToken(aged, 10*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("RenewAgentToken must not error past the ceiling: %v", err)
	}
	if ok {
		t.Error("renewal past the max-lifetime ceiling must be refused (ok=true)")
	}
	if renewed != "" {
		t.Error("renewal past the ceiling must not mint a token")
	}
}

// TestRenewAgentTokenNoCeiling: a non-positive ceiling disables the bound —
// renewal always succeeds regardless of age.
func TestRenewAgentTokenNoCeiling(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0.Add(1000 * time.Hour) }
	aged, err := a.mintAgentToken(agentIdentity(), 10*time.Minute, t0)
	if err != nil {
		t.Fatalf("mintAgentToken: %v", err)
	}
	_, ok, err := a.RenewAgentToken(aged, 10*time.Minute, 0)
	if err != nil {
		t.Fatalf("RenewAgentToken: %v", err)
	}
	if !ok {
		t.Error("a non-positive ceiling must never refuse renewal")
	}
}

// TestRenewAgentTokenRejectsInvalidToken: renewal validates the incoming token
// exactly as AuthenticateAgent does; a tampered/foreign token is refused with an
// error, never silently re-minted.
func TestRenewAgentTokenRejectsInvalidToken(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	other := NewJWTAuthenticator(nil, "different-secret", time.Hour)
	foreign, err := other.IssueAgentToken(agentIdentity(), 10*time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if _, ok, err := a.RenewAgentToken(foreign, 10*time.Minute, 24*time.Hour); err == nil || ok {
		t.Errorf("renewing a foreign-signed token must fail; got ok=%v err=%v", ok, err)
	}
}
