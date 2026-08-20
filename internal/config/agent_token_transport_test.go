package config

import "testing"

// TestAgentTokenTransportDefault: a fresh config binds
// auth.agent_token_transport=envvar, so a default deploy keeps the plaintext
// agent-token env var — the exchange transport is strictly opt-in.
func TestAgentTokenTransportDefault(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if got := c.Auth.AgentTokenTransport; got != AgentTokenTransportEnvVar {
		t.Errorf("auth.agent_token_transport default = %q, want %q", got, AgentTokenTransportEnvVar)
	}
}

// TestAgentTokenTransportEnvBinds proves the key binds from
// LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT (registered in serverDefaults so
// AutomaticEnv sees it).
func TestAgentTokenTransportEnvBinds(t *testing.T) {
	t.Setenv("LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT", "exchange")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if got := c.Auth.AgentTokenTransport; got != AgentTokenTransportExchange {
		t.Errorf("auth.agent_token_transport = %q, want %q (from env)", got, AgentTokenTransportExchange)
	}
}

// TestValidateAgentTokenTransport locks the enum allowlist: valid values (and
// empty = the envvar default) pass; an unknown value fails closed at boot.
func TestValidateAgentTokenTransport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport string
		wantErr   bool
	}{
		{"empty defaults", "", false},
		{"envvar", AgentTokenTransportEnvVar, false},
		{"exchange", AgentTokenTransportExchange, false},
		{"unknown", "magic", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Auth.JWT.Secret = "set"
			c.Auth.AgentTokenTransport = tc.transport
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("transport=%q: Validate() = nil, want error", tc.transport)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("transport=%q: Validate() = %v, want nil", tc.transport, err)
			}
		})
	}
}
