# Phase 5.1 — Airflow UI Auth Wiring

## Goal

Implement the auth surface that the unmodified Apache Airflow 3.2.1 React UI
expects, so the UI can complete login, identify the user, and render its menu
shaped by Leoflow's capabilities.

By the end of this sub-phase:

1. The UI bundle is reachable from the Leoflow server (asset serving works).
2. The UI completes login against Leoflow successfully.
3. The UI calls /ui/auth/me and gets a valid identity response.
4. The UI calls /ui/auth/menus and renders ONLY the menu sections Leoflow
   actually supports (Dags, Browse Task Instances, Browse DAG Runs). Sections
   for Assets, Connections, Variables, Pools, Backfills, Admin do NOT appear.

End-to-end testable: open the UI in a browser, log in, see the curated menu.

## Prerequisites

- Phase 4 complete.
- Read docs/ui-compatibility.md fully before starting.
- Authoritative spec (single source of truth for shapes, do not guess):
  apache/airflow tag 3.2.1 →
  airflow-core/src/airflow/api_fastapi/core_api/openapi/_private_ui.yaml

## Constraints

- TDD strict (ADR 0011), A+ floor (ADR 0012), supply chain clean (ADR 0014),
  GoDocs mandatory. Coverage floor 80% per new package.
- No SPA modifications, no forks. Bundle consumed verbatim.
- Every response shape must match the version-pinned spec exactly.

## Deliverables

1. **ADR 0017 — Static Asset Serving Strategy.** Server embeds the 3.2.1 SPA via
   go:embed (coherent with Scalar/ADR 0013, single binary, no drift). Rejected:
   nginx reverse proxy, runtime CDN download.
2. **Makefile `fetch-airflow-ui`** — pull apache/airflow:3.2.1, copy the static
   dist out of the container to internal/ui/assets/, write VERSION (=3.2.1).
   Deterministic, idempotent; commit the result.
3. **Embed assets** — internal/ui/{assets,embed.go(go:embed),handler.go}. Serve
   static files with proper MIME (.js/.css/.svg/.png/.woff2/.map); SPA fallback
   to index.html; cache headers (hashed assets immutable+long max-age,
   index.html no-cache). Wire at '/', excluding /api,/ui,/auth,/healthz,/readyz,
   /metrics,/docs.
4. **/ui/auth/* endpoints** (shapes pinned to 3.2.1):
   - POST /ui/auth/token: body {username,password} → 200 token (delegate to
     existing authTokenHandler); 401 problem+json. Keep /auth/token too.
   - GET /ui/auth/me: requires auth; AuthenticatedMeResponse shape.
   - GET /ui/auth/menus: requires auth; MenuItemCollectionResponse; return ONLY
     supported items (Dags, Browse>DAG Runs, Browse>Task Instances). Exclude
     Assets, Connections, Variables, Pools, Backfills, Plugins, Providers, Admin,
     Configurations, XComs-as-top-level.
5. **TDD order:** handler tests (index.html, hashed asset, SPA fallback, /api not
   intercepted); /ui/auth/token (valid/invalid/rate-limit/shape); /ui/auth/me
   (valid/no-header/expired); /ui/auth/menus (curated set, excludes others,
   schema-valid).
6. **Spec validation in tests** — embed _private_ui.yaml
   (internal/api/specs/airflow_3_2_1_private_ui.yaml), validate every /ui response
   against its schema.
7. **Docs** — docs/ui-compatibility.md "Phase 5.1 Implementation" section.
8. **CHANGELOG** — docs/CHANGELOG.md [Unreleased]/Added: Phase 5.1 bundle + auth
   wiring + curated menu.
9. **E2E** (test/e2e/ui_auth_test.go, //go:build e2e): chromedp/playwright →
   navigate /, login admin/<bootstrap>, assert curated menu, screenshot
   docs/screenshots/ui-phase-5-1-menu.png. This is the source of truth for done.

## Out of scope (→ 5.2/5.3)

/ui/dags, /ui/grid/*, /ui/structure/* (5.2); /ui/config + degradation stubs
(5.3); /api/v2/dags/{id}/details (5.2). Post-login landing failing on missing
/ui/dags is EXPECTED. 5.1 success = login + curated menu visible.

## Acceptance

ADR 0017; fetch-airflow-ui works; bundle committed; go:embed picks it up; server
serves SPA at /; /ui/auth/token,me,menus correct (curated menu); ≥80% coverage;
E2E logs in via browser + asserts menu; lint A+; govulncheck clean; screenshot
committed; test→feat commits.

## Attention point (founder)

Riskiest: the "curated menu hides features" strategy. If the 3.2.1 UI ignores
/ui/auth/menus and calls e.g. /ui/connections directly → 404 + console noise
(ugly, not fatal). The 5.3 empty stubs cover this, so the phase order is correct.
