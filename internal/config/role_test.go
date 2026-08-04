package config

import "testing"

func TestServerRoleDefaultsToAll(t *testing.T) {
	c := &ServerConfig{}
	if got := c.Server.EffectiveRole(); got != RoleAll {
		t.Errorf("empty role should default to %q, got %q", RoleAll, got)
	}
}

func TestServerRolePredicates(t *testing.T) {
	for _, tc := range []struct {
		role                       string
		servesAPI, servesScheduler bool
	}{
		{"all", true, true},
		{"", true, true}, // empty == all (Lite / backward-compat)
		{"api", true, false},
		{"scheduler", false, true},
	} {
		s := ServerSection{Role: tc.role}
		if s.ServesAPI() != tc.servesAPI {
			t.Errorf("role %q: ServesAPI = %v, want %v", tc.role, s.ServesAPI(), tc.servesAPI)
		}
		if s.ServesScheduler() != tc.servesScheduler {
			t.Errorf("role %q: ServesScheduler = %v, want %v", tc.role, s.ServesScheduler(), tc.servesScheduler)
		}
	}
}

func TestServerRoleValidationRejectsUnknown(t *testing.T) {
	c := &ServerConfig{Server: ServerSection{Role: "worker"}}
	if err := c.validateRole(); err == nil {
		t.Error("an unknown role must be rejected loudly")
	}
	for _, ok := range []string{"", "all", "api", "scheduler"} {
		c := &ServerConfig{Server: ServerSection{Role: ok}}
		if err := c.validateRole(); err != nil {
			t.Errorf("role %q should be valid, got %v", ok, err)
		}
	}
}
