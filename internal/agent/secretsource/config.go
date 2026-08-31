package secretsource

import (
	"encoding/json"
	"fmt"
)

// BackendConfig is the operator-supplied external-secrets backend configuration,
// read pod-side from the LEOFLOW_SECRETS_* env the control plane injects (operator
// only — the dispatch filter keeps an author's task env from setting LEOFLOW_
// keys, #828). It is never author-influenced.
type BackendConfig struct {
	// Class is the provider backend class the in-pod resolver subprocess
	// instantiates (e.g. the Airflow AWS SecretsManagerBackend).
	Class string
	// Kwargs is the raw backend kwargs JSON, passed to the subprocess on stdin
	// (never argv/env — a shared PID namespace makes those task-readable).
	Kwargs json.RawMessage
	// Routing is the declaration-scope predicate (Covers) + name→provider-id
	// mapping (SecretID), derived from the kwargs prefixes.
	Routing Backend
}

// ParseBackendConfig builds the operator backend config from the class name and
// raw kwargs JSON. enabled is false when no class is set (no external backend →
// the resolver is never built and the chain is vault-only). A kind is covered iff
// its `*_prefix` kwarg is present, mirroring Airflow's enable-by-prefix. Malformed
// kwargs JSON is an error (fail closed at config time, not at resolve time).
func ParseBackendConfig(class, kwargsJSON string) (cfg BackendConfig, enabled bool, err error) {
	if class == "" {
		return BackendConfig{}, false, nil
	}
	raw := json.RawMessage("{}")
	var prefixes struct {
		ConnectionsPrefix *string `json:"connections_prefix"`
		VariablesPrefix   *string `json:"variables_prefix"`
	}
	if kwargsJSON != "" {
		if uerr := json.Unmarshal([]byte(kwargsJSON), &prefixes); uerr != nil {
			return BackendConfig{}, false, fmt.Errorf("parsing LEOFLOW_SECRETS_BACKEND_KWARGS: %w", uerr)
		}
		raw = json.RawMessage(kwargsJSON)
	}
	routing := Backend{}
	if prefixes.ConnectionsPrefix != nil {
		routing.Connections = true
		routing.ConnectionsPrefix = *prefixes.ConnectionsPrefix
	}
	if prefixes.VariablesPrefix != nil {
		routing.Variables = true
		routing.VariablesPrefix = *prefixes.VariablesPrefix
	}
	return BackendConfig{Class: class, Kwargs: raw, Routing: routing}, true, nil
}
