# ADR 0008: JWT Authentication with OIDC-Ready Interface

**Status:** Accepted
**Date:** 2026-05-21

## Context

Authentication for an orchestrator must serve two audiences:

- **MVP users:** small teams, often single-tenant, who want it to "just work" with minimal setup.
- **Enterprise users (target for v1.x):** require SSO via OIDC providers (Okta, Azure AD, Google Workspace, Keycloak), MFA, audit logging.

Implementing OIDC properly is 3-4 weeks of work with per-provider edge cases. Implementing JWT bearer tokens is one week.

## Decision

The MVP ships **JWT bearer authentication** as the only working auth mechanism. However, all internals are structured around an `Authenticator` interface so that OIDC, LDAP, or other providers can be plugged in later without refactoring the API layer.

The full RBAC schema (users, roles, permissions, tenant scoping) ships from day one, even though it is exercised by a single static role in the MVP.

## How It Works

### Token Issuance

`POST /auth/token` with a JSON body `{"username": ..., "password": ...}`. The server validates against the `users` table (passwords hashed with bcrypt) and returns:

```json
{
  "access_token": "eyJhbGc...",
  "token_type": "bearer",
  "expires_in": 3600
}
```

The JWT payload contains:

```json
{
  "sub": "user-uuid",
  "tenant_id": "default",
  "roles": ["admin"],
  "exp": 1716307200,
  "iat": 1716303600,
  "iss": "leoflow"
}
```

JWTs are signed with HS256 using a secret loaded from configuration (`LEOFLOW_JWT_SECRET`).

### Token Validation

A Gin middleware runs on every protected endpoint:

1. Extract `Authorization: Bearer <token>` from the request.
2. Validate signature and expiration.
3. Load the user and roles from the JWT claims.
4. Attach to the request context.

### RBAC Schema (Ready for v1.x)

```
tenants(id, name, created_at)
users(id, tenant_id, email, password_hash, oidc_subject, oidc_provider, created_at)
roles(id, tenant_id, name, description)
permissions(id, action, resource)
role_permissions(role_id, permission_id)
user_roles(user_id, role_id)
```

The MVP creates a single `admin` role with all permissions on a single `default` tenant. The schema is ready for multi-tenant, fine-grained RBAC.

### OIDC Hook Point

The `Authenticator` interface:

```go
type Authenticator interface {
    Authenticate(ctx context.Context, token string) (*User, error)
    IssueToken(ctx context.Context, creds Credentials) (string, error)
}
```

The MVP implementation is `JWTAuthenticator`. A future `OIDCAuthenticator` plugs in via configuration:

```yaml
auth:
  provider: jwt | oidc | ldap
  oidc:
    issuer_url: https://accounts.google.com
    client_id: ...
    client_secret: ...
```

No API contract changes. No refactor.

## Rationale

- **OIDC is too expensive for the MVP.** Per-provider quirks (Microsoft's tenant routing, Keycloak's realm structure, etc.) take weeks to handle correctly.
- **JWT alone is enterprise-acceptable for the MVP era.** Many internal tools use JWT + service-account-style tokens.
- **The schema cost is paid once.** Adding `tenant_id` everywhere later requires a full database migration. Adding it on day one costs nothing.

## Consequences

- The CLI must have an `auth` subcommand to create users and tokens: `leoflow auth create-user`, `leoflow auth create-token`.
- The bootstrap process creates a default admin user on first boot, prints credentials to logs once. Operators rotate immediately.
- Token rotation: tokens are short-lived (1 hour default). The MVP does not yet implement refresh tokens; users re-authenticate. v1.1 adds refresh.
- The Airflow UI's `/auth/token` flow is matched exactly so the UI works without changes.

## Security Notes

- Passwords are hashed with bcrypt, cost 12.
- JWT secret rotation requires a flag day (all existing tokens invalidated). v1.1 will support multi-secret validation for graceful rotation.
- Failed login attempts are rate-limited per IP (configurable, default 5 per minute).
- All auth events emit structured audit logs.

## Alternatives Rejected

- **Full OIDC in MVP:** rejected as too expensive.
- **No auth at all in MVP ("trust the network"):** rejected as it makes the path to enterprise impossible.
- **Auth as a separate proxy (e.g., oauth2-proxy):** rejected because it complicates the deployment story for solo users.
