package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/version"
)

// dbtGroupProjectDirs returns the distinct project directories the dbt_groups
// of a dag.py DAG need baked into the image, cleaned and in a stable order.
//
// Sorted rather than ranged: DbtGroups is a map and Go randomizes map
// iteration, so emitting in range order would give a different Dockerfile on
// every compile — a different image digest from an unchanged project, and a
// cold layer cache every time. (ADR 0003 argues for per-DAG images; it says
// nothing about reproducibility, so this is the engineering reason, not a
// citation.) Deduplicated because two groups may cover
// one project with different granularity or selectors, and a repeated COPY is a
// wasted layer that reads like a bug in a diff.
//
// The cleaned form is what the baked flag resolves against: dbtProjectDir
// returns the value verbatim, so `--project-dir ./transform` from WORKDIR
// /home/leoflow lands on /home/leoflow/transform — which is where
// filepath.Clean puts the COPY destination.
func dbtGroupProjectDirs(cfg *domain.LeoflowConfig) []string {
	seen := make(map[string]bool, len(cfg.DbtGroups))
	dirs := make([]string, 0, len(cfg.DbtGroups))
	for _, group := range cfg.DbtGroups {
		if group == nil || group.Project == "" {
			continue
		}
		project := filepath.Clean(group.Project)
		if seen[project] {
			continue
		}
		seen[project] = true
		dirs = append(dirs, project)
	}
	slices.Sort(dirs)
	return dirs
}

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
	return baseImageRef(publishedBaseRepo, cfg.PythonVersion, version.Get().Version)
}

// baseImageRef composes the task base image reference for a CLI of version
// cliVersion. It pins the immutable per-release base (repo:py<ver>-v<X.Y.Z>) so a
// compile from a release reproduces byte-for-byte (ADR 0003); a dev/dirty/`git
// describe` build has no published versioned base and falls back to the moving
// repo:py<ver> line. The published tag is ALWAYS `py<ver>-v<X.Y.Z>` (release.yaml
// stamps `github.ref_name`, the git tag WITH its leading `v`), but GoReleaser
// stamps the CLI's version WITHOUT the `v` (`{{ .Version }}`), so normalize to
// exactly one leading `v` — otherwise a released CLI would FROM a `py<ver>-X.Y.Z`
// tag that was never pushed.
func baseImageRef(repo, pythonVersion, cliVersion string) string {
	base := repo + ":py" + pythonVersion
	if tag := releaseBaseTag(cliVersion); tag != "" {
		return base + "-v" + strings.TrimPrefix(tag, "v")
	}
	return base
}

// devDescribeRe matches the `git describe` suffix a non-release build carries
// (`-<commits>-g<sha>`), which has no published versioned base image.
var devDescribeRe = regexp.MustCompile(`-\d+-g[0-9a-f]+`)

// releaseBaseTag returns v when it names a clean release (a tag the release
// workflow published a py<ver>-<v> base for), or "" for a dev/dirty/describe build
// that has no such base. Keeps a source build working (moving tag) while a real
// release pins its immutable base.
func releaseBaseTag(v string) string {
	if v == "" || v == "dev" || strings.HasSuffix(v, "-dirty") || devDescribeRe.MatchString(v) {
		return ""
	}
	return v
}

// resolveBuildImage decides the image reference for a build. An explicit --image
// flag always wins so a caller can pin any tag. Otherwise the reference is
// derived from the registry block (url/image_name:version); a missing url or
// image_name yields "" so the caller can fail with an actionable message rather
// than building an untagged image.
func resolveBuildImage(flagImage string, cfg *domain.LeoflowConfig, dagVersion string) string {
	if flagImage != "" {
		return flagImage
	}
	if cfg.Registry == nil || cfg.Registry.URL == "" || cfg.Registry.ImageName == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s:%s", strings.TrimRight(cfg.Registry.URL, "/"), cfg.Registry.ImageName, dagVersion)
}

// generatedDockerfile renders the Dockerfile for a project that does not ship its
// own, layering the DAG onto the task base image (ADR 0003). The layers are
// ordered for cache efficiency and matched to leoflow.yaml: FROM the resolved
// base, the apt system_packages, then the pip dependencies (connectors: expanded
// to their provider packages, ADR 0038), and finally the DAG source COPY with the
// agent's PYTHONPATH convention. An unknown connector name is a hard error
// (surfaced from EffectiveDependencies) rather than a runtime ModuleNotFoundError.
//
// For a dbt project (cfg.Dbt set, ADR 0042) the source is the dbt project
// directory, not a dag.py: the final layer COPYs that directory to the workdir
// and sets no PYTHONPATH, since dbt ships no importable Python module.
func generatedDockerfile(cfg *domain.LeoflowConfig, dagSource string) (string, error) {
	deps, err := cfg.EffectiveDependencies()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", resolveBaseImage(cfg))
	// apt requires root, and pip as the base's non-root USER (65532) falls back to a
	// `--user` install whose console scripts (e.g. `dbt`) land in ~/.local/bin, which
	// is not on PATH — so a synthesized dbt image would fail `dbt: command not found`.
	// Install as root (system site → /usr/local/bin on PATH), then drop back to the
	// non-root runtime USER as the LAST instruction, so the image's final USER is a
	// numeric non-root UID (#852). That is what the KUBELET resolves at container
	// creation when a task pod sets runAsNonRoot with no runAsUser — buildSecurityContext
	// sets exactly that pair — and a root image fails CreateContainerConfigError,
	// which reconcile.go matches by name. PodSecurity admission never reads the
	// image; it only checks the PodSpec.
	//
	// The drop being last is not what makes the copied source read-only to the
	// task. COPY without --chown lands uid=0 gid=0 whatever USER is active —
	// measured against a real build, both before and after a USER instruction —
	// so the ownership holds regardless of where the drop sits.
	rootForInstall := len(cfg.SystemPackages) > 0 || len(deps) > 0
	if rootForInstall {
		b.WriteString("USER root\n")
	}
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
	if cfg.Dbt != nil {
		// A dbt project is the DAG source (ADR 0042): there is no dag.py to COPY and
		// no Python module to import, so COPY the project directory (dbt_project.yml
		// + models/ + baked manifest.json) to the workdir and set no PYTHONPATH. The
		// task runs `dbt --project-dir <project>` from WORKDIR /home/leoflow, so the
		// project must land at /home/leoflow/<project> matching the baked --project-dir.
		// The project is read-only to the task because COPY lands it root-owned, not
		// because of where it sits relative to the USER drop: dbt writes target/,
		// logs/, and profiles.yml to /tmp (base ENV), never the project (#852).
		project := filepath.Clean(cfg.Dbt.Project)
		fmt.Fprintf(&b, "COPY %s /home/leoflow/%s\n", project, project)
		if rootForInstall {
			b.WriteString("USER 65532:65532\n")
		}
		return b.String(), nil
	}
	// A dag.py DAG, with or without dbt task groups (ADR 0043). Both the DAG
	// source and every group's project have to be in the image: a group's tasks
	// run `dbt --project-dir <project>` from WORKDIR /home/leoflow, so a project
	// that was never COPYed makes every dbt task exit within seconds of pod
	// start — after a green compile and a green Lite run, because Lite's
	// subprocess executor reads from disk and never needs the image (#20).
	base := filepath.Base(dagSource)
	groups := dbtGroupProjectDirs(cfg)
	if slices.Contains(groups, ".") {
		// project: "." means the dbt project IS the DAG directory. render.go
		// omits --project-dir for that value, so dbt runs from WORKDIR and the
		// whole context must land at /home/leoflow — one COPY that already
		// carries dag.py and subsumes every other group directory.
		b.WriteString("COPY . /home/leoflow/\n")
	} else {
		fmt.Fprintf(&b, "COPY %s /home/leoflow/%s\n", base, base)
		for _, project := range groups {
			fmt.Fprintf(&b, "COPY %s /home/leoflow/%s\n", project, project)
		}
	}
	b.WriteString("ENV PYTHONPATH=/home/leoflow\n")
	if rootForInstall {
		b.WriteString("USER 65532:65532\n")
	}
	return b.String(), nil
}

// dockerignoreName is the only ignore-file form every builder reads.
//
// BuildKit also honors `<dockerfile>.dockerignore`, which would be tidier — it
// needs no cleanup and cannot touch a file the user owns. It is not usable
// here, for two measured reasons:
//
//   - the legacy builder ignores it completely. Built with DOCKER_BUILDKIT=0
//     against a context holding a `.leoflow.generated.Dockerfile.dockerignore`
//     listing `.env`, the `.env` landed in the image. A silent leak on a
//     builder the operator can still select (`--builder`, or podman) is worse
//     than no feature.
//   - where it IS honored it REPLACES the context `.dockerignore` rather than
//     adding to it. With a user `.dockerignore` excluding `big.bin` and a
//     per-Dockerfile file excluding `.env`, `big.bin` shipped. We would have
//     closed one leak by reopening whatever the user had closed.
const dockerignoreName = ".dockerignore"

// dockerignoreHeader marks the block this tool appends, so a repeated merge
// after an interrupted build does not stack identical comments.
const dockerignoreHeader = "# added by leoflow compile --build from exclude_paths (leoflow.yaml); removed after the build"

// ensureDockerignore materializes exclude_paths as a .dockerignore for the
// duration of the build, and restores the workspace afterward.
//
// exclude_paths has been in the schema, defaulted, and documented as "skipped
// both in image build and workspace discovery" while having zero consumers in
// build code (#995). With `project: "."` — mode 1's documented default — the
// generated Dockerfile is a single `COPY . /home/leoflow/`, so the whole
// context is baked: measured in a built image, that included a `.env` holding a
// warehouse password, a BYO `profiles.yml`, `.git`, and the generated
// Dockerfile the build had just written.
//
// The user's own .dockerignore is preserved and comes FIRST, so their file is
// merged rather than replaced. Ours goes last because later rules win in
// .dockerignore syntax, and leoflow.yaml is the authoritative statement of what
// may leave in the image: a stray `!secrets/x` in a .dockerignore must not
// silently defeat an `exclude_paths: [secrets/]` the author wrote deliberately.
//
// The original content is held in the closure rather than a sibling backup
// file, so nothing extra can enter the context. If the process dies mid-build
// the workspace is left with the MERGED file — a superset of the user's, so it
// excludes more and never less. That is the right direction to fail in.
func ensureDockerignore(w io.Writer, dir string, cfg *domain.LeoflowConfig) (cleanup func(), err error) {
	noop := func() {}
	path := filepath.Join(dir, dockerignoreName)

	original, rerr := os.ReadFile(path) //nolint:gosec // G304: dir is the user's own project directory.
	had := rerr == nil
	if rerr != nil && !os.IsNotExist(rerr) {
		return noop, fmt.Errorf("reading %s: %w", path, rerr)
	}

	// Concatenated into a fresh slice: append onto cfg.ExcludePaths would write
	// through to the caller's config whenever that slice has spare capacity.
	patterns := slices.Concat(cfg.ExcludePaths, dbtBuildArtifacts(cfg))
	warnUnexcludedSecrets(w, dir, cfg, patterns)
	merged, changed := mergeDockerignore(original, patterns)
	if !changed {
		return noop, nil
	}
	//nolint:gosec // G703: `path` is filepath.Join(dir, <const>), and dir is the
	// project directory the operator pointed `leoflow compile` at. Writing into
	// it is the whole point — ensureDockerfile writes the generated Dockerfile to
	// the same place, for the same reason.
	if werr := os.WriteFile(path, merged, 0o600); werr != nil {
		return noop, fmt.Errorf("writing %s: %w", path, werr)
	}
	return func() {
		if had {
			//nolint:errcheck,gosec // best-effort restore; 0600 matches the write above.
			_ = os.WriteFile(path, original, 0o600)
			return
		}
		_ = os.Remove(path) //nolint:errcheck // best-effort cleanup of a file we created
	}, nil
}

// dbtBuildArtifacts lists what a host-side `dbt parse` leaves inside each dbt
// project directory that is provably never read back, scoped to those
// directories.
//
// The compile runs `dbt parse` on the host, that parse writes into the project
// it parsed, and a wholesale COPY then bakes the result (#1013). Measured on an
// image the e2e builds:
//
//   - `.user.yml` is dbt's anonymous-usage cookie: a stable UUID identifying
//     the BUILD HOST's dbt user, shared with everyone who pulls the image, and
//     read by the in-pod dbt so every pod reports as that same user.
//   - `logs/dbt.log` carries absolute host paths — four of them in that build.
//     That is the #993 class (a build-host path baked into a published
//     artifact) arriving through a door the entrypoint assertions do not watch.
//
// **Only those two.** An earlier version of this also excluded `target/`,
// `dbt_packages/` and `profiles.yml`, and the dbt e2e caught all three as
// regressions — correctly, because each of them CAN be a deliberate input:
//
//   - `target/` holds the manifest that `dbt.manifest` points at. The field is
//     documented as "a pre-built manifest.json (the Pro/CI baked path)", and
//     the e2e's own fixture sets `manifest: target/manifest.json`.
//   - `dbt_packages/` is where `dbt deps` installs. A project that resolves
//     dependencies on the build host and bakes them is doing something
//     reasonable and reproducible.
//   - `profiles.yml` is the BYO-profiles pattern: ship your own and point
//     DBT_PROFILES_DIR at it. The runtime generates a profiles.yml from a
//     Leoflow connection when it HAS one — it does not have one here, and
//     "the runtime always generates its own" was a claim read off a single
//     code path rather than checked against the configurations that exist.
//
// profiles.yml is credential-shaped, so it is warned about instead — the same
// trade as .env: tell the author, do not decide for them.
//
// Scoped per project directory rather than added to the default ExcludePaths,
// because `logs` is an ordinary name a non-dbt project may want shipped. A
// project with no dbt gets none of these.
func dbtBuildArtifacts(cfg *domain.LeoflowConfig) []string {
	dirs := dbtGroupProjectDirs(cfg)
	if cfg.Dbt != nil && cfg.Dbt.Project != "" {
		project := filepath.Clean(cfg.Dbt.Project)
		if !slices.Contains(dirs, project) {
			dirs = append(dirs, project)
		}
	}
	slices.Sort(dirs)

	artifacts := []string{"logs", ".user.yml"}
	out := make([]string, 0, len(dirs)*len(artifacts))
	for _, dir := range dirs {
		for _, a := range artifacts {
			if dir == "." {
				out = append(out, a)
				continue
			}
			out = append(out, dir+"/"+a)
		}
	}
	return out
}

// secretishNames are files whose presence in a build context is nearly always a
// mistake, and whose contents are nearly always a credential.
//
// They are NOT excluded. Each of them can be a legitimate input — a DAG calling
// load_dotenv() reads `.env` at run time, and a dbt project may ship its own
// profiles.yml with DBT_PROFILES_DIR pointed at it — so dropping one silently
// would break that project with a failure far from its cause. Choosing for the
// author is the wrong trade; letting a password ship unnoticed is also the
// wrong trade; a warning is the only option that is neither.
var secretishNames = []string{".env", ".env.local", ".netrc", "credentials.json", "service-account.json", "id_rsa", "profiles.yml"}

// warnUnexcludedSecrets prints a note for any secret-looking file the build
// context still carries after exclude_paths is applied.
//
// Looks in the context root and in each dbt project directory. Not a recursive
// walk: that is slow on an arbitrary project and noisy, and those are the two
// places these files actually live — profiles.yml in particular belongs next to
// dbt_project.yml, which is not the root when the project is a subdirectory.
func warnUnexcludedSecrets(w io.Writer, dir string, cfg *domain.LeoflowConfig, patterns []string) {
	excluded := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		excluded[strings.TrimSpace(p)] = true
	}

	places := []string{"."}
	places = append(places, dbtGroupProjectDirs(cfg)...)
	if cfg.Dbt != nil && cfg.Dbt.Project != "" {
		places = append(places, filepath.Clean(cfg.Dbt.Project))
	}

	seen := make(map[string]bool)
	for _, place := range places {
		for _, name := range secretishNames {
			rel := name
			if place != "." {
				rel = place + "/" + name
			}
			if seen[rel] || excluded[rel] {
				continue
			}
			seen[rel] = true
			info, err := os.Stat(filepath.Join(dir, rel))
			if err != nil || info.IsDir() {
				continue
			}
			//nolint:errcheck // a warning that cannot be delivered must not fail the build
			fmt.Fprintf(w, "warning: %s is in the build context and will be baked into the image, "+
				"which is pushed to a registry and pulled by every pod that runs this DAG. "+
				"If it holds credentials, add %q to exclude_paths in leoflow.yaml.\n", rel, rel)
		}
	}
}

// mergeDockerignore appends the patterns to the existing content, skipping any
// already present, and reports whether anything was added. The generated
// Dockerfile is always excluded: it is written into the context by
// ensureDockerfile and has no business inside the image it builds.
func mergeDockerignore(original []byte, excludes []string) (merged []byte, changed bool) {
	existing := make(map[string]bool)
	for _, line := range strings.Split(string(original), "\n") {
		existing[strings.TrimSpace(line)] = true
	}

	want := make([]string, 0, len(excludes)+1)
	for _, p := range excludes {
		p = strings.TrimSpace(p)
		if p != "" && !existing[p] {
			want = append(want, p)
			existing[p] = true
		}
	}
	if !existing[generatedDockerfileName] {
		want = append(want, generatedDockerfileName)
	}
	if len(want) == 0 {
		return original, false
	}

	var b strings.Builder
	if len(original) > 0 {
		b.Write(original)
		if !strings.HasSuffix(string(original), "\n") {
			b.WriteString("\n")
		}
	}
	// The header is written once. A build killed before cleanup leaves the merged
	// file behind (documented on ensureDockerignore), and the next build merges
	// onto it — patterns already present are skipped, so without this check the
	// only thing that accumulated was a stack of identical comments.
	if !strings.Contains(string(original), dockerignoreHeader) {
		b.WriteString(dockerignoreHeader)
		b.WriteString("\n")
	}
	for _, p := range want {
		b.WriteString(p)
		b.WriteString("\n")
	}
	return []byte(b.String()), true
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
