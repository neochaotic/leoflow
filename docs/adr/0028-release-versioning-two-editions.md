# ADR 0028: Release & Versioning for the Two Editions (One Tag, Two Co-Versioned Artifacts)

**Status:** Accepted (effective from the first stable release)
**Date:** 2026-05-27
**Deciders:** Project founder
**Relates:** ADR 0027 (product editions), ADR 0014 (supply chain security)

## Context

One codebase produces two delivery artifacts (ADR 0027):

- **Lite** — installable binaries (`leoflow` / `leoflow-server` / `leoflow-agent`)
  published via GoReleaser and an `install.sh`.
- **Pro** — container image(s) (`leoflow-server`, `leoflow-agent`, migrate) plus a
  **Helm chart**, deployed on Kubernetes.

We need a versioning and release strategy that guarantees the two artifacts stay
compatible and are delivered together, without a "which Lite works with which
Pro" matrix. Today (pre-alpha) only Lite is published; the Pro image + chart
pipeline is not live yet (Helm hardening is in progress). This ADR fixes the
strategy to adopt when we cut **stable** versions / first publish Pro.

## Decision

1. **One git tag = one version = both artifacts.** From the *same commit*, the
   release pipeline builds the Lite binaries **and** the Pro images + Helm chart,
   all stamped with that version. They are co-versioned, never released from
   different commits.

2. **Helm `appVersion` = the release tag** (= the image tag). The chart's own
   `version` tracks the tag **in lockstep** initially, and is decoupled only if
   the chart later needs a cadence independent of the app (a template-only fix) —
   the standard `appVersion` (software) vs `version` (chart) split.

3. **Atomic release.** The tag pipeline publishes all artifacts only if **all**
   gates pass — Lite `install-smoke` across the supported distros, Pro
   `chart-lint` + a `kind`/`k3d` install test + image vulnerability scans. Any
   failure **retracts the release to draft** (as the Lite install-smoke already
   does). No partial releases (e.g. Lite without its matching Pro image).

4. **Build once, promote.** Images are built once and referenced by immutable
   tag/digest; the chart references the image by that tag/digest. The chart is
   published as an **OCI artifact in the same registry as the images**
   (`oci://<registry>/charts/leoflow:<version>`), tying chart and image together.

5. **Supply chain (extends ADR 0014).** Images and the chart carry an SBOM and
   are signed (cosign), on top of the existing Trivy / govulncheck / CodeQL gates.

6. **Compatibility contract.** *Lite vX ⇔ Pro vX* — same commit, same `/api/v2`
   surface, same DAG format. A DAG authored on Lite vX runs on Pro vX.

## Timing

**This ADR is the agreed target, effective from the first stable release.**
During pre-alpha, only the Lite binaries are published; the Pro image + chart
pipeline and the co-versioned, atomic release land when Pro is first published
(tracked with the Helm hardening work). Until then the strategy is documented but
not yet enforced.

## Consequences

- **Simplicity & support:** one changelog, one version to ask about, and
  compatibility guaranteed by construction (same commit in both editions).
- **Version churn:** a release bumps both artifacts even when only one edition
  changed. Accepted as the price of a single, unambiguous version line; the
  `appVersion`/`version` split is the escape hatch if the chart must move alone.
- **One pipeline to maintain:** the tag workflow fans out to binaries + images +
  chart and gates them together; more build surface, but a single source of truth
  for "what shipped."
- **No drift:** because the chart pins the image by tag/digest from the same tag,
  a deployed Pro cluster always runs exactly the code of its release.
