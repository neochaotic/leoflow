package config

import (
	"strings"
	"testing"
)

func oidcEmptyNamesBase() *ServerConfig {
	c := &ServerConfig{}
	c.UI.Edition = "pro"
	c.Auth.Provider = AuthProviderOIDC
	c.Auth.OIDC.Issuer = "https://login.example.com"
	c.Auth.OIDC.ClientID = "client"
	c.Auth.OIDC.RedirectURL = "https://app.example.com/api/v2/auth/oidc/callback"
	c.Auth.OIDC.TenantClaim = "hd"
	c.Auth.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
	return c
}

// TestValidateOIDCRejectsEmptyMapNames closes the last shape in the family of
// configurations that boot green and deny logins.
//
// `corp.example:` with nothing after it is valid YAML that binds to the empty
// string, and the boot check only ever asked whether the map was non-EMPTY. An
// empty tenant name resolves a login to a tenant that cannot exist; an empty
// role name is copied straight into the resolved role set by MapRoles and then
// fails RoleExists. Both deny the login behind the same generic answer as
// everything else.
//
// This one fails boot rather than warning, unlike its neighbors, and the reason
// is that it cannot be a transient. The other checks ask a live database, where
// "absent" and "could not ask" are the same answer under a blip and a hard gate
// would turn a lagging replica into a CrashLoopBackOff. This is a string in a
// file: an empty name is never correct, no deployment works today with one, and
// the answer is the same on every boot.
func TestValidateOIDCRejectsEmptyMapNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*ServerConfig)
		want  string
	}{
		{"an empty tenant name", func(c *ServerConfig) {
			c.Auth.OIDC.TenantClaims = map[string]string{"corp.example": ""}
		}, "auth.oidc.tenant_claims"},
		{"a blank tenant name", func(c *ServerConfig) {
			c.Auth.OIDC.TenantClaims = map[string]string{"corp.example": "   "}
		}, "auth.oidc.tenant_claims"},
		{"an empty claim value as the key", func(c *ServerConfig) {
			c.Auth.OIDC.TenantClaims = map[string]string{"": "default"}
		}, "auth.oidc.tenant_claims"},
		{"an empty role name", func(c *ServerConfig) {
			c.Auth.OIDC.RoleMappings = map[string]string{"data-eng": ""}
		}, "auth.oidc.role_mappings"},
		{"an empty group as the key", func(c *ServerConfig) {
			c.Auth.OIDC.RoleMappings = map[string]string{"": "editor"}
		}, "auth.oidc.role_mappings"},
		{"a blank default_role", func(c *ServerConfig) {
			c.Auth.OIDC.DefaultRole = "  "
		}, "auth.oidc.default_role"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := oidcEmptyNamesBase()
			tc.apply(c)
			err := c.validateOIDC()
			if err == nil {
				t.Fatal("boot accepted a name that can never resolve, and every login it governs is denied with nothing naming this")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error does not name the setting to fix: %v", err)
			}
		})
	}

	t.Run("names that resolve are accepted", func(t *testing.T) {
		c := oidcEmptyNamesBase()
		c.Auth.OIDC.RoleMappings = map[string]string{"data-eng": "editor"}
		c.Auth.OIDC.DefaultRole = "viewer"
		if err := c.validateOIDC(); err != nil {
			t.Errorf("rejected a correct configuration: %v", err)
		}
	})

	t.Run("an absent default_role is not an empty one", func(t *testing.T) {
		if err := oidcEmptyNamesBase().validateOIDC(); err != nil {
			t.Errorf("an unset default_role is the documented strict posture, not a typo: %v", err)
		}
	})
}
