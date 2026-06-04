package cli

import (
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

func TestRequireRegistryRejectsUnconfigured(t *testing.T) {
	// ApplyDefaults instantiates Registry but leaves URL empty — deploy must
	// still reject it, loudly and actionably (ADR 0041).
	cfg := &domain.LeoflowConfig{}
	cfg.ApplyDefaults()

	err := requireRegistry(cfg)
	if err == nil {
		t.Fatal("expected requireRegistry to reject a config with no registry URL")
	}
	msg := err.Error()
	for _, want := range []string{"registry", "leoflow.yaml", "docker login"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got:\n%s", want, msg)
		}
	}
}

func TestRequireRegistryRejectsMissingImageName(t *testing.T) {
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/org"}}
	if err := requireRegistry(cfg); err == nil {
		t.Error("expected requireRegistry to reject a registry URL with no image_name")
	}
}

func TestRequireRegistryAcceptsConfigured(t *testing.T) {
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/org", ImageName: "etl"}}
	if err := requireRegistry(cfg); err != nil {
		t.Errorf("requireRegistry on a configured registry: %v", err)
	}
}
