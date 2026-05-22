package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const tokenIssuer = "leoflow"

// jwtClaims is the Leoflow JWT payload.
type jwtClaims struct {
	TenantID string   `json:"tenant_id"`
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

// Authenticate validates a bearer token and reconstructs the user from its claims.
func (a *JWTAuthenticator) Authenticate(_ context.Context, token string) (*User, error) {
	var c jwtClaims
	parsed, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return nil, errors.Join(ErrInvalidToken, err)
	}
	return &User{ID: c.Subject, TenantID: c.TenantID, Roles: c.Roles}, nil
}

func (a *JWTAuthenticator) sign(user *User) (string, error) {
	now := time.Now()
	c := jwtClaims{
		TenantID: user.TenantID,
		Roles:    user.Roles,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			Issuer:    tokenIssuer,
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
