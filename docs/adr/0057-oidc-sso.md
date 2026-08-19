# ADR 0057: OIDC/SSO authentication with fail-closed tenant pinning

**Status:** Proposed
**Date:** 2026-08-19
**Relates:** ADR 0008 (JWT auth — the `Authenticator` seam OIDC plugs into; the `_token` this flow mints and the per-request reload that governs live permissions), ADR 0035 (keyless-first — the verify path uses the issuer's public JWKS, no secret), ADR 0055 (token liveness/revocation — bounds the OIDC session lifetime), ADR 0014 (supply chain — the new `go-oidc` dependency), ADR 0049 (edition gating precedent), ADR 0018 (embedded login UI the `_token` cookie drives)

## Context

Leoflow authenticates with HS256 JWTs (`internal/auth/jwt.go`): a
username/password `POST /auth/token` mints a token carrying
`{tenant_id, email, roles}`, and the middleware accepts it as a bearer or the
`_token` cookie. The `Authenticator` interface was documented from the start as
keeping OIDC/LDAP pluggable, and the `users` table already carries
`oidc_subject`/`oidc_provider` (unique) with a nullable `password_hash` — the
schema anticipated SSO, but no code used those columns and `auth.provider: oidc`
booted closed as unimplemented.

The enterprise pivot needs SSO against Entra ID / Google Cloud Identity / Okta,
with **fail-closed** mapping of external identities to Leoflow tenants and roles.
OIDC is a **login flow**, not a new request-path authenticator: the callback
verifies the IdP's ID token and mints Leoflow's own `_token`, so the middleware,
`/ui/auth/*`, and the API are unchanged. The per-request reload from ADR 0008's
prerequisite work (roles/permissions/`is_active` reloaded on every verify)
remains the authority for live permissions and gives revocation-within-TTL.

An identity/zero-trust specialist review of the design flagged five HIGH issues;
all five are folded into the decisions below (H1→D6, H2→D8, H3→relies on the
role-ladder prerequisite, H4→D10, H5→the audit decision).

## Decisions

**D1. Flow = Authorization Code + PKCE.** Endpoints `GET /api/v2/auth/oidc/login`
and `GET /api/v2/auth/oidc/callback`, registered under the existing public
`/api/v2/auth/` prefix. The `state`, `nonce`, and PKCE `code_verifier` are carried
in a short-lived, signed, `HttpOnly; Secure; SameSite=Lax` cookie (stateless — no
new session store). The cookie is signed with a key **derived** from the app's
HS256 secret, distinct from the session-token key, so a state cookie can never
cross-verify as a session token. Implicit flow is rejected.

**D2. Session = the `_token` JWT cookie.** On success the callback mints
Leoflow's own `_token` via `MintUserToken` and sets it **server-side**
`HttpOnly; Secure; SameSite=Lax` (hardened vs the credential login page's
client-JS cookie). The OIDC session length equals the JWT TTL; mid-session
revocation is the per-request `is_active` reload (revocation-within-TTL). The
post-login redirect reuses `sanitizeNext` (open-redirect safe).

**D3. Identity match = `(oidc_provider, oidc_subject)`, email only as a verified
link key.** `FindUserByOIDCSubject` resolves a returning user by the immutable
pair (loading roles + permissions + `is_active`, mirroring the credential path).
`oidc_provider` is the pinned **issuer URL** — stable and unambiguous per
deployment. `email` is trusted only when `email_verified == true` (absent ⇒
false).

**D4. JIT provisioning = OFF by default (`auth.oidc.jit_provisioning: false`).**
Default requires a pre-existing user row (no accidental over-privilege). When ON,
a first OIDC login creates a `users` row (email + oidc_subject/provider, NULL
password) with the roles from D5. When OFF and no row matches, the login is
rejected (403) — never auto-created.

**D5. Group→role mapping = explicit config map to existing DB role names,
default-DENY, IdP-authoritative.** `auth.oidc.role_mappings: {<idp_group_value>:
<leoflow_role>}`. An unmapped group grants no role. On every login the mapped set
is written to the DB `user_roles` as the user's EXACTLY-current roles (see the
reconciliation consequence below) and also carried in the minted token; the
per-request reload then turns those DB roles into permissions. The identity
provider therefore stays authoritative for an OIDC user's roles.

**D5a. `default_role` (opt-in UX softener).** `auth.oidc.default_role` softens
default-deny **without weakening the secure default**: when an authenticated
user resolves to zero mapped roles and `default_role` is set, they are granted
that single fallback role (operators are advised to use a read-only role such as
`viewer`). Empty (the default) keeps strict deny — an unmapped user gets no role.
The role must exist for the resolved tenant; an unknown `default_role` fails the
login closed (audited), on both the pre-provisioned and the JIT paths.

**D6. Tenant pin = issuer-locked + IdP claim (`tid`/`hd`), fail-closed. NOT
email-domain (H1).** (a) the config pins the org's single-tenant **issuer URL**
and every ID token whose `iss` differs is rejected; (b) the tenant is resolved
from an IdP-issued claim — Entra `tid`, Google Workspace `hd` — via
`auth.oidc.tenant_claims: {<value>: <tenant_name>}`; (c) `email_verified == true`
is required before any email is trusted; (d) an absent/unmapped claim or an
issuer mismatch is a **403 that never falls back to `default`**. Email domain is
never the pin (spoofable under Entra `/common` or "any Google account").

**D6a. `allowed_email_domains` (login-level allowlist, layered on the pin — NOT
the pin).** `auth.oidc.allowed_email_domains` gates **every** OIDC login
(pre-provisioned and JIT). It runs **only after** the issuer pin, the `tid`/`hd`
pin, and `email_verified == true` have passed, so the email domain is trustworthy
at that point. Empty imposes no restriction (the `tid`/`hd` pin is the sole
boundary). Non-empty admits a login only when the verified email's domain is in
the list; every other login is 403 (audited). It is configured at install time
(env `LEOFLOW_AUTH_OIDC_ALLOWED_EMAIL_DOMAINS` / Helm value). It does **not**
substitute for `tid`/`hd` — H1 stays closed.

**D7. `provider: jwt` stays default; `oidc` is opt-in AND Pro-gated.**
`validateProvider` allows `provider: oidc` only when `ui.edition == "pro"` AND
the required config is present (issuer, client_id, redirect_url, https issuer);
otherwise boot fails closed with an actionable message. The JWT secret stays
required because the callback mints the app's own `_token`.

**D8. Break-glass = designated local admin(s) only (H2).** When
`provider: oidc`, the credential path `POST /auth/token` accepts ONLY the emails
in `auth.oidc.break_glass_emails`; every other password login is rejected. Each
break-glass attempt (allowed, denied, bad-credentials) is audited. Default posture
is effectively SSO-only. `dev_no_auth` stays loopback-only. In JWT mode the
credential path is ungated and unchanged.

**D9. Verify path is keyless (ADR 0035-aligned).** ID-token verification uses the
IdP's public OIDC discovery + JWKS — no secret. The `client_secret` is used only
for the code exchange, injected via `LEOFLOW_AUTH_OIDC_CLIENT_SECRET` (env, never
persisted, never logged). All `auth.oidc.*` keys are registered in
`serverDefaults` so viper binds the scalar `LEOFLOW_AUTH_OIDC_*` env vars; the
map/slice leaves (role_mappings, tenant_claims, allowed_email_domains,
break_glass_emails) are config-file / Helm-values driven.

**D10. Dependency = `github.com/coreos/go-oidc/v3` (+ `golang.org/x/oauth2`,
promoted to direct).** go-oidc verifies the signature (against the discovered
JWKS), the audience, and the issuer, and its `NewProvider` pins the issuer at
discovery. It does **not** verify nonce, azp, the callback state/CSRF binding, or
clock skew — these are enforced explicitly in Leoflow code (H4): `state` bound to
and compared against the signed cookie; RFC 9207 `iss` response param checked
where present; `aud == client_id`; `azp == client_id` when present; `nonce`
against the state cookie (constant-time); and `exp`/`iat`/`nbf` with a small
configurable clock skew (default 60s), which is why go-oidc's leeway-less expiry
check is disabled and this package is the single time authority.

**Audit (H5).** Every auth event is recorded to the existing audit sink
(`RecordAuthEvent`, alongside `RecordUserCreatedAudit`): login success/failure,
tenant-pin rejection, JIT provisioning, break-glass (allowed/denied), each with
actor/email/tenant/outcome and a non-secret reason. Tokens and the client secret
are never recorded. Audit is best-effort — a sink error never changes a security
outcome (a 403 stays a 403, a success stays a success). Events that never
resolved a tenant (a tenant-pin rejection) fall back to the `default` tenant so
the security event still lands, with the attempted values in the metadata.

## Consequences

- New public redirect endpoints; one new direct dependency; `x/oauth2` promoted
  to direct.
- The self-asserted tenant is closed off for OIDC logins (the JWT/password path
  is unchanged). Email-domain is a secondary, opt-in allowlist, never the pin.
- Session length is bounded by the JWT TTL; full logout propagation is deferred
  with the rest of ADR 0055. The per-request `is_active` reload gives
  revocation-within-TTL.
- The identity provider is authoritative for an OIDC user's roles. On every
  successful login — for both JIT-created and returning users — the user's DB
  `user_roles` are reconciled to EXACTLY the group→role-mapped set (D5), falling
  back to `[default_role]` when the mapping is empty and one is set, or to no
  roles otherwise. The per-request reload then reads that freshly-written set, so
  an IdP demotion or deprovisioning takes effect on the next login. The
  consequence is that a manual DB role grant for an OIDC user is overwritten by
  the next login: OIDC users' roles are managed through IdP group membership, not
  through admin DB grants. A role name (mapped or `default_role`) that does not
  exist in the tenant fails the login closed and is audited, rather than silently
  granting nothing.

## Security review — folded in

H1 → D6 (issuer-lock + `tid`/`hd`, never email-domain as the pin; D6a adds the
domain allowlist strictly on top). H2 → D8 (break-glass scoped to an explicit
allowlist, audited). H3 → the role model relies on the seeded role ladder and the
per-request permission reload already in place. H4 → D10 (explicit
nonce/azp/state/skew/iss checks, not assumed from the library). H5 → the audit
decision. Verification order is fixed and fail-closed at every step:
signature/audience/issuer → nonce → azp → clock skew → subject → email_verified →
tenant pin → email-domain allowlist.
