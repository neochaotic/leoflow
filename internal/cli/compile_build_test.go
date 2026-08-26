package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

// TestResolveBaseImageExplicitWins verifies an explicit base_image in the config
// is used verbatim, overriding the python_version-derived default.
func TestResolveBaseImageExplicitWins(t *testing.T) {
	cfg := &domain.LeoflowConfig{BaseImage: "ghcr.io/acme/base:custom", PythonVersion: "3.12"}
	if got := resolveBaseImage(cfg); got != "ghcr.io/acme/base:custom" {
		t.Errorf("resolveBaseImage() = %q, want the explicit base_image", got)
	}
}

// TestResolveBaseImageDefaultsToPublishedBase verifies the base image defaults to
// the PUBLISHED runtime base (ghcr.io/neochaotic/leoflow-runtime:py<version>) when
// base_image is unset, so a yaml-driven build produces an image that builds
// anywhere — no locally-built leoflow-base needed.
func TestResolveBaseImageDefaultsToPublishedBase(t *testing.T) {
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11"}
	if got := resolveBaseImage(cfg); got != "ghcr.io/neochaotic/leoflow-runtime:py3.11" {
		t.Errorf("resolveBaseImage() = %q, want ghcr.io/neochaotic/leoflow-runtime:py3.11", got)
	}
}

// TestResolveBuildImageFlagWins verifies the --image flag overrides the registry
// config so a caller can always pin an explicit tag.
func TestResolveBuildImageFlagWins(t *testing.T) {
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/acme", ImageName: "etl"}}
	if got := resolveBuildImage("myreg/explicit:v1", cfg, "abc123"); got != "myreg/explicit:v1" {
		t.Errorf("resolveBuildImage() = %q, want the explicit flag value", got)
	}
}

// TestResolveBuildImageFromRegistry verifies the image is derived from the
// registry block (url/image_name:version) when no --image flag is given.
func TestResolveBuildImageFromRegistry(t *testing.T) {
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/acme", ImageName: "etl"}}
	if got := resolveBuildImage("", cfg, "v1.2.3"); got != "ghcr.io/acme/etl:v1.2.3" {
		t.Errorf("resolveBuildImage() = %q, want ghcr.io/acme/etl:v1.2.3", got)
	}
}

// TestResolveBuildImageEmptyWhenNothing verifies that with neither a flag nor a
// complete registry block the resolver returns "" so the caller can error.
func TestResolveBuildImageEmptyWhenNothing(t *testing.T) {
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/acme"}}
	if got := resolveBuildImage("", cfg, "v1"); got != "" {
		t.Errorf("resolveBuildImage() = %q, want empty (image_name missing)", got)
	}
}

// TestGeneratedDockerfileLayers verifies the generated Dockerfile layers in the
// canonical order: FROM base, system packages, pip dependencies (connectors
// expanded), then the DAG source COPY with PYTHONPATH.
func TestGeneratedDockerfileLayers(t *testing.T) {
	cfg := &domain.LeoflowConfig{
		PythonVersion:  "3.11",
		SystemPackages: []string{"git"},
		Connectors:     []string{"postgres"},
		Dependencies:   []string{"pandas==2.2.0"},
	}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	for _, want := range []string{
		"FROM ghcr.io/neochaotic/leoflow-runtime:py3.11",
		"apt-get install",
		"git",
		"pip install",
		"apache-airflow-providers-postgres",
		"pandas==2.2.0",
		"COPY dag.py /home/leoflow/dag.py",
		"ENV PYTHONPATH=/home/leoflow",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("generatedDockerfile() missing %q in:\n%s", want, df)
		}
	}
}

// TestGeneratedDockerfileDbtCopiesProject verifies that for a dbt project the
// generated Dockerfile COPYs the dbt project directory (dbt_project.yml + models/
// + baked manifest.json) to the workdir instead of the nonexistent dag.py, and
// does not set a PYTHONPATH pointing at a Python module that dbt does not ship
// (#769). The dbt task runs `dbt --project-dir <project>` from WORKDIR
// /home/leoflow, so the project must land at /home/leoflow/<project>.
func TestGeneratedDockerfileDbtCopiesProject(t *testing.T) {
	cfg := &domain.LeoflowConfig{
		PythonVersion: "3.11",
		Dependencies:  []string{"dbt-duckdb==1.8.0"},
		Dbt:           &domain.DbtConfig{Project: "analytics"},
	}
	// A dbt-only project defaults DagSource to dag.py, which does not exist —
	// the pre-fix COPY dag.py is what fails the build.
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	if !strings.Contains(df, "COPY analytics /home/leoflow/analytics") {
		t.Errorf("generatedDockerfile() should COPY the dbt project dir, got:\n%s", df)
	}
	if strings.Contains(df, "dag.py") {
		t.Errorf("generatedDockerfile() must not reference dag.py for a dbt project:\n%s", df)
	}
	if strings.Contains(df, "PYTHONPATH") {
		t.Errorf("generatedDockerfile() must not set PYTHONPATH for a dbt project:\n%s", df)
	}
	// The dependency layer (dbt adapter) is still emitted before the COPY.
	if !strings.Contains(df, "pip install") || !strings.Contains(df, "dbt-duckdb==1.8.0") {
		t.Errorf("generatedDockerfile() should keep the pip dependency layer:\n%s", df)
	}
}

// TestGeneratedDockerfileDbtProjectPathCleaned verifies a project declared with a
// leading ./ lands at a clean /home/leoflow/<project> path matching the baked
// --project-dir, so `dbt --project-dir ./transform` resolves inside the image.
func TestGeneratedDockerfileDbtProjectPathCleaned(t *testing.T) {
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11", Dbt: &domain.DbtConfig{Project: "./transform"}}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	if !strings.Contains(df, "COPY transform /home/leoflow/transform") {
		t.Errorf("generatedDockerfile() should COPY the cleaned project path, got:\n%s", df)
	}
}

// TestEnsureDockerfileDbtUsesExisting verifies a dbt project that ships its own
// Dockerfile still wins — ensureDockerfile honors it verbatim and never generates.
func TestEnsureDockerfileDbtUsesExisting(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(existing, []byte("FROM ghcr.io/acme/dbt:custom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11", Dbt: &domain.DbtConfig{Project: "analytics"}}
	path, cleanup, err := ensureDockerfile(dir, "Dockerfile", cfg, "dag.py")
	if err != nil {
		t.Fatalf("ensureDockerfile() error = %v", err)
	}
	defer cleanup()
	if path != existing {
		t.Errorf("path = %q, want the existing Dockerfile %q", path, existing)
	}
}

// TestGeneratedDockerfileUnknownConnectorErrors verifies a typo'd connector name
// fails generation loudly (it cannot resolve to a pip package) rather than
// silently producing an image that ModuleNotFoundErrors at run time.
func TestGeneratedDockerfileUnknownConnectorErrors(t *testing.T) {
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11", Connectors: []string{"nope_not_a_connector"}}
	if _, err := generatedDockerfile(cfg, "dag.py"); err == nil {
		t.Fatal("generatedDockerfile() expected an error for an unknown connector, got nil")
	}
}

// TestEnsureDockerfileUsesExisting verifies a project-supplied Dockerfile is used
// as-is (no generation) and is not removed by the cleanup.
func TestEnsureDockerfileUsesExisting(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(existing, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11"}
	path, cleanup, err := ensureDockerfile(dir, "Dockerfile", cfg, "dag.py")
	if err != nil {
		t.Fatalf("ensureDockerfile() error = %v", err)
	}
	defer cleanup()
	if path != existing {
		t.Errorf("path = %q, want the existing Dockerfile %q", path, existing)
	}
	cleanup()
	if _, statErr := os.Stat(existing); statErr != nil {
		t.Errorf("cleanup removed the user's Dockerfile: %v", statErr)
	}
}

// TestEnsureDockerfileGeneratesWhenAbsent verifies that with no Dockerfile present
// one is generated from the config, and the cleanup removes the generated file.
func TestEnsureDockerfileGeneratesWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11", Dependencies: []string{"pandas==2.2.0"}}
	path, cleanup, err := ensureDockerfile(dir, "Dockerfile", cfg, "dag.py")
	if err != nil {
		t.Fatalf("ensureDockerfile() error = %v", err)
	}
	data, readErr := os.ReadFile(path) //nolint:gosec // test reads a path it just created
	if readErr != nil {
		t.Fatalf("reading generated Dockerfile: %v", readErr)
	}
	if !strings.Contains(string(data), "FROM ghcr.io/neochaotic/leoflow-runtime:py3.11") {
		t.Errorf("generated Dockerfile missing FROM line:\n%s", data)
	}
	cleanup()
	if _, statErr := os.Stat(path); statErr == nil {
		t.Errorf("cleanup did not remove the generated Dockerfile %q", path)
	}
}
