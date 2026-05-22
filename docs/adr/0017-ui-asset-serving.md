# ADR 0017: UI Static Asset Serving Strategy

**Status:** Accepted
**Date:** 2026-05-22
**Deciders:** Project founder

## Context

Phase 5 serves the unmodified Apache Airflow 3.2.1 React SPA from the Leoflow
control plane (see `docs/ui-compatibility.md`). The compiled bundle (HTML, hashed
JS/CSS chunks, fonts, source maps — roughly 5–10 MB minified) must be delivered
to the browser, and the SPA's client-side router requires an `index.html`
fallback for unknown paths.

We need to decide how the `leoflow-server` binary obtains and serves these
assets.

## Decision

The Leoflow server **embeds the pinned Airflow 3.2.1 SPA bundle via `go:embed`**
and serves it from the root path `/`.

- The bundle lives under `internal/ui/assets/`, committed to the repository, with
  a `VERSION` marker file containing the exact upstream tag (`3.2.1`).
- It is produced reproducibly by `make fetch-airflow-ui`, which extracts the
  `dist` directory from the `apache/airflow:3.2.1` image.
- `internal/ui/embed.go` embeds the directory; `internal/ui/handler.go` serves
  files under their original paths with correct MIME types and cache headers
  (hashed assets `immutable`, `index.html` `no-cache`), falling back to
  `index.html` for non-file paths (SPA routing).
- The server reserves `/api`, `/ui`, `/auth`, `/healthz`, `/readyz`, `/metrics`,
  `/docs`, and `/openapi` for their existing handlers; everything else falls to
  the UI handler.

## Rationale

- **One static-asset pattern.** Consistent with embedding the Scalar API docs
  (ADR 0013) — the binary already embeds and serves static UI assets.
- **Single-binary deployment.** No reverse proxy (nginx) to operate; the control
  plane serves the API and the UI from one process.
- **No version drift.** The bundle is versioned alongside the binary; a given
  build always ships the UI it was tested against, pinned to 3.2.1.
- **Acceptable size.** A 5–10 MB embedded bundle is fine for a server binary
  (the agent's tiny-binary constraint in ADR 0004 does not apply to the server).

## Consequences

- The bundle is a committed binary blob; upgrades are a deliberate event
  (re-run `make fetch-airflow-ui`, bump `VERSION`, retest), matching the pinned
  compatibility posture.
- The build requires the assets directory to exist; a placeholder `index.html`
  is committed so the build works before `make fetch-airflow-ui` populates the
  real bundle.
- Serving the UI and the API from one origin avoids CORS for UI calls.

## Alternatives Rejected

- **Reverse proxy (nginx) serving assets + proxying `/api` and `/ui`:**
  operational complexity (a second component to deploy and configure) for
  marginal benefit over `go:embed`.
- **Runtime download from a CDN/registry:** adds a runtime network dependency and
  a failure mode at startup; breaks air-gapped and single-binary deployment.
