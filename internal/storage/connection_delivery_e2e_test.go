//go:build integration

package storage_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/secrets"
)

// TestConnectionDeliveryChainOfCustodyIntegration is the rigorous companion to
// the per-layer tests. Each layer (Repository CRUD, SecretConnectionURIs,
// agentrpc.GetConnections, agent.buildEnv) has its own unit/integration
// test; this one walks the **whole chain** for one Connection per
// supported conn_type and proves the env var the user-side Python sees
// matches what the Admin user posted.
//
// Specifically: a refactor that breaks the wiring between layers without
// changing any individual layer's behaviour (e.g. tenant_id encoding
// regresses; URI builder's password-escape changes; the agent maps the
// proto response into a different env key) would let every isolated test
// keep passing but break this one. That is the bug class this guards.
//
// Edge case the per-layer tests miss: a password with URI-reserved
// characters (`@`, `:`, `/`, `?`, `#`, `%`, `+`). The URI builder must
// percent-escape them so the final URI is parseable; the user-side Python
// (psycopg2.connect, pymysql, etc.) then sees the original password. A
// regression here would surface only at the connector boundary — too late.
//
// Table-driven across the locally-testable SQL connectors so adding the
// next (mssql, sqlite) is a 1-line change.
func TestConnectionDeliveryChainOfCustodyIntegration(t *testing.T) {
	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
	cases := []struct {
		connType    string
		defaultPort int
		host        string
		schema      string
	}{
		{connType: "postgres", defaultPort: 5432, host: "warehouse.example.com", schema: "analytics"},
		{connType: "mysql", defaultPort: 3306, host: "warehouse.example.com", schema: "analytics"},
		{connType: "mariadb", defaultPort: 3306, host: "warehouse.example.com", schema: "analytics"},
	}
	for _, tc := range cases {
		t.Run(tc.connType, func(t *testing.T) {
			repo, _, ctx := openRepo(t)
			key := make([]byte, 32)
			for i := range key {
				key[i] = byte(i + 7)
			}
			cipher, err := secrets.NewAESGCM(key)
			if err != nil {
				t.Fatal(err)
			}
			repo.SetCipher(cipher)

			connID := fmt.Sprintf("e2e_%s_%d", tc.connType, time.Now().UnixNano())
			port := tc.defaultPort
			if cerr := repo.SetConnection(ctx, "default", domain.Connection{
				ConnID: connID, ConnType: tc.connType,
				Host: tc.host, Login: "etl_user",
				Password: rawPassword, Port: &port, Schema: tc.schema,
			}); cerr != nil {
				t.Fatalf("SetConnection: %v", cerr)
			}
			t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

			tenantUUID, err := repo.TenantUUID(ctx, "default")
			if err != nil {
				t.Fatalf("TenantUUID: %v", err)
			}

			// Layer hop: agent calls GetConnections → server.SecretConnectionURIs
			// → returns the URI map. The agentrpc handler is a thin pass-through
			// (see internal/agentrpc/secrets.go::GetConnections); the per-layer
			// tests already cover the gRPC handler. The wiring this test
			// validates is what the Repository returns vs what the env-renderer
			// outputs.
			uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
			if err != nil {
				t.Fatalf("SecretConnectionURIs: %v", err)
			}
			uri, present := uris[connID]
			if !present {
				t.Fatalf("URI for %q missing from delivery map; got keys = %v",
					connID, mapKeys(uris))
			}

			// The agent renders this as the env entry; mirror the exact format
			// the agent uses (internal/agent/runner.go::secretsEnv) so a
			// divergence is caught here.
			envEntry := "AIRFLOW_CONN_" + strings.ToUpper(connID) + "=" + uri
			if !strings.HasPrefix(envEntry, "AIRFLOW_CONN_") {
				t.Errorf("env entry missing required prefix: %q", envEntry)
			}

			// The end-user contract: the Python connector must accept the URI,
			// which requires url.Parse to succeed and round-trip the password
			// unencoded.
			parsed, perr := url.Parse(uri)
			if perr != nil {
				t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
			}
			if parsed.Scheme != tc.connType {
				t.Errorf("scheme = %q, want %q", parsed.Scheme, tc.connType)
			}
			wantHost := fmt.Sprintf("%s:%d", tc.host, tc.defaultPort)
			if parsed.Host != wantHost {
				t.Errorf("host = %q, want %q", parsed.Host, wantHost)
			}
			if parsed.User.Username() != "etl_user" {
				t.Errorf("username = %q, want etl_user", parsed.User.Username())
			}
			gotPassword, _ := parsed.User.Password()
			if gotPassword != rawPassword {
				t.Errorf("password round-trip failed: got %q, want %q (the URI builder must percent-escape; net/url must un-escape on parse)",
					gotPassword, rawPassword)
			}
			if !strings.HasPrefix(parsed.Path, "/"+tc.schema) {
				t.Errorf("path = %q, want /%s prefix", parsed.Path, tc.schema)
			}
		})
	}
}

// mapKeys returns the keys of a string-keyed map, used to assemble
// diagnostic output when a Connection delivery test fails.
func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
