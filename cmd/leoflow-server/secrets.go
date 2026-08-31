package main

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/neochaotic/leoflow/internal/agent/secretsource"
	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/storage"
)

// configureSecrets wires connection-secret encryption (the AES-256-GCM cipher)
// and the external-secrets D6 registration relaxation (ADR 0060) onto the repo.
func configureSecrets(repo *storage.Repository, cfg *config.ServerConfig, logger *slog.Logger) error {
	if err := configureSecretCipher(repo, cfg.SecretKey, logger); err != nil {
		return err
	}
	return configureSecretsCoverage(cfg.Secrets, repo)
}

// secretsKwargsJSON serializes the operator's backend kwargs to the JSON string
// delivered to the pod (and parsed for routing). encoding/json sorts map keys, so
// the output is deterministic (no pod-spec churn). A marshal never fails for
// map[string]string; on the impossible error it falls back to an empty object.
func secretsKwargsJSON(sec config.SecretsSection) string {
	if len(sec.BackendKwargs) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(sec.BackendKwargs)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// backendCoverage adapts an operator secretsource.Backend to the storage layer's
// external-secret coverage predicate (ADR 0060 B1/D6). Coverage is kind-level and
// operator-derived; a DAG can never influence it.
type backendCoverage struct{ b secretsource.Backend }

func (c backendCoverage) CoversVariable(string) bool { return c.b.Covers(secretsource.KindVariable) }
func (c backendCoverage) CoversConnection(string) bool {
	return c.b.Covers(secretsource.KindConnection)
}

// configureSecretsCoverage relaxes the D6 registration check so a declared name
// covered by the configured external backend registers without a provider call
// (ADR 0060 B1). No-op when no backend is configured; fails closed on a malformed
// config rather than silently disabling it.
func configureSecretsCoverage(sec config.SecretsSection, repo *storage.Repository) error {
	cfg, enabled, err := secretsource.ParseBackendConfig(sec.Backend, secretsKwargsJSON(sec))
	if err != nil {
		return fmt.Errorf("secrets backend config: %w", err)
	}
	if !enabled {
		return nil
	}
	repo.SetExternalSecretCoverage(backendCoverage{b: cfg.Routing})
	return nil
}
