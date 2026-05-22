// Package auth provides JWT authentication, password hashing, the RBAC
// permission model, and login rate limiting for the control plane (ADR 0008).
package auth

import (
	"context"
	"errors"
)

// ErrInvalidCredentials is returned when a username/password pair is rejected.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrInvalidToken is returned when a token is malformed, expired, or unsigned by us.
var ErrInvalidToken = errors.New("invalid token")

// Permission is an action on a resource (e.g. {Action: "read", Resource: "dag"}).
type Permission struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// User is an authenticated principal with its tenant, roles, and permissions.
type User struct {
	ID          string
	TenantID    string
	Email       string
	Roles       []string
	Permissions []Permission
}

// HasPermission reports whether the user may perform action on resource. The
// admin role, or an admin action / wildcard resource permission, grants access.
func (u *User) HasPermission(action, resource string) bool {
	for _, r := range u.Roles {
		if r == "admin" {
			return true
		}
	}
	for _, p := range u.Permissions {
		if (p.Action == action || p.Action == "admin") && (p.Resource == resource || p.Resource == "*") {
			return true
		}
	}
	return false
}

// Credentials are the inputs to token issuance.
type Credentials struct {
	Tenant   string
	Username string
	Password string
}

// Authenticator issues and validates authentication tokens. The MVP ships a
// JWT implementation; the interface keeps OIDC/LDAP pluggable (ADR 0008).
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*User, error)
	IssueToken(ctx context.Context, creds Credentials) (string, error)
}

// UserStore loads users for authentication. storage implements it.
type UserStore interface {
	FindUserByLogin(ctx context.Context, tenant, username string) (user *User, passwordHash string, err error)
}
