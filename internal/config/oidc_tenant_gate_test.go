package config

import (
	"strings"
	"testing"
)

// oidcBase returns a config that satisfies every check validateOIDC performed
// before the tenant pin was gated: Pro edition, https issuer, client id and an
// https redirect URL. Each case below removes exactly one thing from it.
func oidcBase() *ServerConfig {
	c := &ServerConfig{}
	c.UI.Edition = "pro"
	c.Auth.Provider = "oidc"
	c.Auth.JWT.Secret = "set"
	c.Auth.OIDC.Issuer = "https://idp.example.com"
	c.Auth.OIDC.ClientID = "leoflow"
	c.Auth.OIDC.RedirectURL = "https://leoflow.example.com/api/v2/auth/oidc/callback"
	c.Auth.OIDC.TenantClaim = "tid"
	c.Auth.OIDC.TenantClaims = map[string]string{"t-123": "default"}
	return c
}

// TestValidateOIDCRequiresTheTenantPin is the boot half of #1143.
//
// Verify resolves a tenant on every login and fails closed when the claim is
// unset or unmapped, so a deployment missing either one rejects 100% of logins
// with a generic 403 while the real reason goes only to the audit log, which in
// an SSO-only deployment nobody can log in to read.
//
// validateOIDC promises exactly the opposite in its own doc comment: that such a
// deployment "fails boot with an actionable message rather than starting a login
// flow that cannot complete". It checked issuer, client_id and redirect_url and
// left out the two settings that actually decide whether a login can succeed.
//
// This matters most under Helm, where tenant_claims is a map that viper cannot
// bind from an env var and the chart ships no config file to carry it, so the
// unsatisfiable state is the DEFAULT one rather than a misconfiguration.
func TestValidateOIDCRequiresTheTenantPin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*ServerConfig)
		wantKey string
	}{
		{
			name:    "no tenant claim",
			mutate:  func(c *ServerConfig) { c.Auth.OIDC.TenantClaim = "" },
			wantKey: "auth.oidc.tenant_claim",
		},
		{
			name:    "claim set but nothing mapped",
			mutate:  func(c *ServerConfig) { c.Auth.OIDC.TenantClaims = nil },
			wantKey: "auth.oidc.tenant_claims",
		},
		{
			name:    "empty map is the same as no map",
			mutate:  func(c *ServerConfig) { c.Auth.OIDC.TenantClaims = map[string]string{} },
			wantKey: "auth.oidc.tenant_claims",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := oidcBase()
			tc.mutate(c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil; this config boots green and then rejects every login")
			}
			// Naming the key is the whole point: the runtime symptom is a generic
			// 403, so the boot error is the only place an operator can learn which
			// setting to add.
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("Validate() = %v, want it to name %q", err, tc.wantKey)
			}
		})
	}
}

// TestValidateOIDCAcceptsAUsableTenantPin is the other half: the gate must not
// reject a configuration that can actually serve a login. Without this, making
// the test above pass is as easy as refusing oidc outright.
func TestValidateOIDCAcceptsAUsableTenantPin(t *testing.T) {
	if err := oidcBase().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a config that can complete a login", err)
	}
}
