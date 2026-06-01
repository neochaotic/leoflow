package main

import (
	"strings"
	"testing"
)

// TestRefuseInsecureSecretsInProd: a Pro chart deployment (edition=production)
// MUST NOT boot with LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true. The dev-only
// escape hatch is fine on Lite, but inside a real cluster shipping secrets over
// a plaintext gRPC channel is the kind of mistake that should be loud (#58).
//
// Lite (edition=lite) and the default (edition="") still tolerate the flag so
// the local dev loop keeps working.
func TestRefuseInsecureSecretsInProd(t *testing.T) {
	cases := []struct {
		name             string
		edition          string
		allowInsecure    bool
		wantErrSubstring string // empty = expect no error
	}{
		{name: "lite + insecure: allowed (dev/inner loop)", edition: "lite", allowInsecure: true},
		{name: "empty edition + insecure: allowed (dev fallback)", edition: "", allowInsecure: true},
		{name: "production + insecure: REFUSED", edition: "production", allowInsecure: true,
			wantErrSubstring: "LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS"},
		{name: "production + NO insecure: allowed (default secure)", edition: "production", allowInsecure: false},
		{name: "lite + NO insecure: allowed", edition: "lite", allowInsecure: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guardInsecureSecretsForEdition(tc.edition, tc.allowInsecure)
			if tc.wantErrSubstring == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErrSubstring)
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstring) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantErrSubstring)
			}
		})
	}
}
