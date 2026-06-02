package main

import "errors"

// guardInsecureSecretsForEdition refuses the dev-only insecure-secrets escape
// hatch when the chart marks the deployment as the Pro edition (#58, Pro alpha
// blocker). The flag is a deliberate dev convenience for Lite that lets the
// control plane ship secrets over a plaintext gRPC channel — fine on
// localhost, never inside a real cluster.
//
// The Helm chart sets LEOFLOW_UI_EDITION=pro on every Pro install; Lite
// (edition=lite) and the unmarked default (edition="") still allow the flag so
// the inner dev loop keeps working without TLS.
func guardInsecureSecretsForEdition(edition string, allowInsecure bool) error {
	if !allowInsecure {
		return nil
	}
	if edition != "pro" {
		return nil
	}
	return errors.New("LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true is not allowed in the Pro edition: " +
		"a Pro deployment ships secrets only over the TLS-enabled gRPC channel — " +
		"either set agentTLS.enabled=true in the Helm values (with cert-manager) or unset the env var " +
		"(see ADR 0014 and issue #58)")
}
