# Changelog

All notable changes to Leoflow are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

> Work toward **0.1.0-rc1** — a Go control plane that runs DAGs end-to-end and
> serves the embedded Apache Airflow 3.2.1 UI. **Not tagged or released yet:** the
> `0.1.0-rc1` tag will be cut only after the maintainer validates the UI in a real
> browser (the open acceptance step below). Nothing here is published.

### Added

- **Embedded Airflow 3.2.1 UI (Phase 5).** The control plane embeds the pinned
  Airflow 3.2.1 React SPA (`go:embed`, ADR 0017) and serves it at `/`, alongside
  the implemented internal UI API:
  - Auth/identity: `GET /ui/config`, `GET /ui/auth/me`, `GET /ui/auth/menus`
    (curated to the screens Leoflow backs), `POST /ui/auth/token`.
  - Read views: `GET /ui/dags` (latest runs embedded, no N+1), `/ui/dags/{id}/latest_run`,
    `/ui/grid/runs/{id}`, `/ui/grid/structure/{id}`, `/ui/structure/structure_data`,
    `/ui/grid/ti_summaries/{id}` (NDJSON stream with a conditional-GET ETag),
    `GET /api/v2/dags/{id}/details` (cron→English), `GET /api/v2/version`.
  - Graceful degradation: unimplemented `/ui` screens return schema-valid empty
    responses; writes degrade to `501`.
  - Static assets are gzipped; the SPA shell and assets load without auth so the
    login screen is reachable, while `/api/v2` and `/ui` data stay gated.
- **One-command demo.** `docker compose --profile demo up --build` brings up
  Postgres, Redis, and the control plane with the UI; bootstraps an admin user.
  `deploy/Dockerfile.server` builds the single image.
- `make fetch-airflow-ui` extracts the pinned UI bundle from `apache/airflow:3.2.1`.

### Changed

- ADR 0007 (Airflow UI Compatibility) premise refined from Airflow 2.x-style
  `/api/v2` parity to the pinned Airflow 3.x `/ui/*` approach (see ADR 0017,
  ADR 0018, `docs/ui-compatibility.md`).

### Fixed

- The static SPA (shell + assets) is now public, so an unauthenticated first
  visit can load the app and reach the login screen.
- `/ui/auth/me` returns the authenticated user's username (the JWT now carries
  the email claim).

### Notes

- The pinned Airflow UI is a tactical MVP choice; a purpose-built Leoflow UI on
  the stable `/api/v2` is the long-term direction (ADR 0018).
- Browser end-to-end verification (rendering, write-flow paths, screenshots) is
  the remaining Phase 5 acceptance step; see `docs/ui-compatibility.md`.

[Unreleased]: https://github.com/neochaotic/leoflow/commits/main
