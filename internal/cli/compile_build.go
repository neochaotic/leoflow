package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/neochaotic/leoflow/internal/domain"
)

// generatedDockerfileName is the file a yaml-driven build writes its synthesized
// Dockerfile to when the project ships none. The leading dot keeps it out of the
// way; the cleanup removes it after the build so it never lingers in the workspace.
const generatedDockerfileName = ".leoflow.generated.Dockerfile"

// publishedBaseRepo is the published Leoflow task base image repository. A
// yaml-driven build's generated Dockerfile defaults its FROM to this (per Python
// version), so the produced DAG image builds anywhere — no locally-built
// leoflow-base required and the Pro control plane can pull it. This is the real
// pipeline: the user ships dag.py + leoflow.yaml, CI (or a local compile)
// generates the image from the published base and pushes it to Pro.
const publishedBaseRepo = "ghcr.io/neochaotic/leoflow-runtime"

// resolveBaseImage returns the task base image a generated DAG Dockerfile builds
// FROM. An explicit base_image in leoflow.yaml wins; otherwise it defaults to the
// published runtime base (publishedBaseRepo:py<python_version>) so the image is
// reproducible and pullable from any builder, not just a host that ran
// `leoflow lite` to build the local base.
func resolveBaseImage(cfg *domain.LeoflowConfig) string {
	if cfg.BaseImage != "" {
		return cfg.BaseImage
	}
	return publishedBaseRepo + ":py" + cfg.PythonVersion
}

// resolveBuildImage decides the image reference for a build. An explicit --image
// flag always wins so a caller can pin any tag. Otherwise the reference is
// derived from the registry block (url/image_name:version); a missing url or
// image_name yields "" so the caller can fail with an actionable message rather
// than building an untagged image.
func resolveBuildImage(flagImage string, cfg *domain.LeoflowConfig, version string) string {
	if flagImage != "" {
		return flagImage
	}
	if cfg.Registry == nil || cfg.Registry.URL == "" || cfg.Registry.ImageName == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s:%s", strings.TrimRight(cfg.Registry.URL, "/"), cfg.Registry.ImageName, version)
}

// generatedDockerfile renders the Dockerfile for a project that does not ship its
// own, layering the DAG onto the task base image (ADR 0003). The layers are
// ordered for cache efficiency and matched to leoflow.yaml: FROM the resolved
// base, the apt system_packages, then the pip dependencies (connectors: expanded
// to their provider packages, ADR 0038), and finally the DAG source COPY with the
// agent's PYTHONPATH convention. An unknown connector name is a hard error
// (surfaced from EffectiveDependencies) rather than a runtime ModuleNotFoundError.
func generatedDockerfile(cfg *domain.LeoflowConfig, dagSource string) (string, error) {
	deps, err := cfg.EffectiveDependencies()
	if err != nil {
		return "", err
	}
	base := filepath.Base(dagSource)
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", resolveBaseImage(cfg))
	if len(cfg.SystemPackages) > 0 {
		// Single RUN so the apt cache cleanup stays in the same layer as the install.
		fmt.Fprintf(&b, "RUN apt-get update && apt-get install -y --no-install-recommends %s "+
			"&& rm -rf /var/lib/apt/lists/*\n", strings.Join(cfg.SystemPackages, " "))
	}
	if len(deps) > 0 {
		// Dependencies before COPY so the (rarely-changing) layer is cached across
		// edits to the DAG source.
		fmt.Fprintf(&b, "RUN pip install --no-cache-dir %s\n", strings.Join(deps, " "))
	}
	fmt.Fprintf(&b, "COPY %s /home/leoflow/%s\nENV PYTHONPATH=/home/leoflow\n", base, base)
	return b.String(), nil
}

// ensureDockerfile resolves the Dockerfile to build with. A project that ships
// its own (at dir/name) is honored verbatim, with a no-op cleanup. Otherwise a
// yaml-driven Dockerfile is generated from the config into a temporary file in
// dir (so it shares the build context) and the returned cleanup removes it. The
// caller always defers cleanup; a build error still leaves the workspace clean.
func ensureDockerfile(dir, name string, cfg *domain.LeoflowConfig, dagSource string) (path string, cleanup func(), err error) {
	existing := filepath.Join(dir, name)
	if _, serr := os.Stat(existing); serr == nil {
		return existing, func() {}, nil
	}
	content, gerr := generatedDockerfile(cfg, dagSource)
	if gerr != nil {
		return "", func() {}, gerr
	}
	generated := filepath.Join(dir, generatedDockerfileName)
	if werr := os.WriteFile(generated, []byte(content), 0o600); werr != nil {
		return "", func() {}, fmt.Errorf("writing generated Dockerfile %s: %w", generated, werr)
	}
	cleanup = func() { _ = os.Remove(generated) } //nolint:errcheck // best-effort cleanup of a temp file
	return generated, cleanup, nil
}
