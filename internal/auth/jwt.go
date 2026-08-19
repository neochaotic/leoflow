package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const tokenIssuer = "leoflow"

// audienceUser scopes tokens minted for human and API users, distinguishing them
// from agent identity tokens (see audienceAgent).
const audienceUser = "leoflow-user"

// jwtClaims is the Leoflow JWT payload.
type jwtClaims struct {
	TenantID string   `json:"tenant_id"`
	Email    string   `json:"email,omitempty"`
	Roles    []string `json:"roles"`
	jwt.RegisteredClaims
}

// JWTAuthenticator issues and validates HS256 JWTs against a UserStore.
type JWTAuthenticator struct {
	store  UserStore
	secret []byte
	ttl    time.Duration
}

// NewJWTAuthenticator builds a JWTAuthenticator with the given user store,
// HS256 secret, and token lifetime.
func NewJWTAuthenticator(store UserStore, secret string, ttl time.Duration) *JWTAuthenticator {
	return &JWTAuthenticator{store: store, secret: []byte(secret), ttl: ttl}
}

// MintUserToken signs a user JWT directly, without checking credentials against
// a store. It is for trusted in-process callers only — notably `leoflow dev`,
// which runs its own control plane and must register DAGs without a login
// round-trip. The token validates under Authenticate using the same secret.
func MintUserToken(secret string, ttl time.Duration, user User) (string, error) {
	a := &JWTAuthenticator{secret: []byte(secret), ttl: ttl}
	return a.sign(&user)
}

// IssueToken validates the credentials against the store and returns a signed JWT.
func (a *JWTAuthenticator) IssueToken(ctx context.Context, creds Credentials) (string, error) {
	user, hash, err := a.store.FindUserByLogin(ctx, creds.Tenant, creds.Username)
	if err != nil {
		return "", ErrInvalidCredentials
	}
	if !VerifyPassword(hash, creds.Password) {
		return "", ErrInvalidCredentials
	}
	return a.sign(user)
}

// Authenticate validates a bearer token and resolves the current principal.
// After the signature and registered claims check out, it reloads the user's
// roles, permissions, and active flag from the store keyed by the token subject
// (the user id). The store — not the token — is the source of truth for
// authorization: this is what makes non-admin role grants gate anything (the
// token carries roles but never permissions) and lets deactivating a user
// revoke their live tokens within the token TTL.
//
// Two cases fall back to the signed claims instead of the reload: a nil store
// (no data plane bound — the trusted in-process minting context) and a subject
// with no backing row (a directly-minted token, e.g. `leoflow dev`). Any other
// store failure fails closed, so a flaky database cannot silently disable
// revocation.
func (a *JWTAuthenticator) Authenticate(ctx context.Context, token string) (*User, error) {
	var c jwtClaims
	parsed, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceUser), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return nil, errors.Join(ErrInvalidToken, err)
	}
	claimed := &User{ID: c.Subject, TenantID: c.TenantID, Email: c.Email, Roles: c.Roles}
	if a.store == nil {
		return claimed, nil
	}
	user, active, err := a.store.FindUserByID(ctx, c.Subject)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return claimed, nil
		}
		return nil, errors.Join(ErrInvalidToken, err)
	}
	if !active {
		return nil, ErrInvalidToken
	}
	return user, nil
}

func (a *JWTAuthenticator) sign(user *User) (string, error) {
	now := time.Now()
	c := jwtClaims{
		TenantID: user.TenantID,
		Email:    user.Email,
		Roles:    user.Roles,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			Issuer:    tokenIssuer,
			Audience:  jwt.ClaimStrings{audienceUser},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(a.ttl)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(a.secret)
	if err != nil {
		return "", fmt.Errorf("signing token: %w", err)
	}
	return signed, nil
}
