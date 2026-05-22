# ADR 0013: API Documentation via Scalar, Embedded in the Server Binary

**Status:** Accepted
**Date:** 2026-05-21

## Context

Leoflow exposes an HTTP API specified by an OpenAPI 3.1 document (see `docs/api/openapi.yaml`). Operators, integrators, and the Airflow UI all interact with this surface. Good API documentation has two functions:

1. **Reference.** What endpoints exist, what parameters they take, what responses look like.
2. **Exploration.** Trying calls live against a running instance to learn behavior.

The choice of documentation tool affects developer experience meaningfully. The three serious options in 2026 are Swagger UI (the incumbent), Redoc (the polished read-only renderer), and Scalar (the modern entrant combining both).

## Decision

Leoflow uses **Scalar** for API documentation. The Scalar static assets and the OpenAPI YAML are **embedded in the `leoflow-server` Go binary** via `//go:embed` and served from the route `/docs`. Operators access documentation that always matches the running server version, with no separate deployment.

## Why Scalar

| Property | Scalar | Swagger UI | Redoc |
|---|---|---|---|
| Modern design (dark mode default, clean) | ✅ | ❌ | ✅ |
| Interactive "try it out" with built-in client | ✅ | ✅ | ❌ |
| Code samples in multiple languages (Go, Python, curl, etc.) | ✅ | Partial | Partial |
| OpenAPI 3.1 native support | ✅ | ✅ | Partial |
| Single static asset (easy to embed) | ✅ | ❌ (many files) | ✅ |
| Self-host, no SaaS, no cost | ✅ | ✅ | ✅ |

Scalar's built-in code samples are particularly valuable for the Leoflow audience: data engineers and DevOps practitioners who want a `curl` example to copy-paste or a Go snippet they can drop into a tool.

## Why Embed in the Binary

Three concrete reasons:

1. **Version coherence.** Operators reading `/docs` see documentation for the *exact* binary running. No drift between deployed code and deployed docs.
2. **Zero operational burden.** No separate static site, no CI deploy to GitHub Pages to manage, no CDN configuration.
3. **Air-gapped friendly.** Enterprise customers running Leoflow in disconnected environments have full documentation without internet access.

The cost is a few hundred KB added to the binary, which is negligible.

## Implementation Sketch

```go
// internal/api/docs/docs.go

//go:embed scalar/scalar.standalone.js
var scalarJS []byte

//go:embed openapi.yaml
var openAPISpec []byte

// Handler returns an HTTP handler that serves the Scalar-rendered
// API documentation at the root and the raw OpenAPI spec at /openapi.yaml.
func Handler() http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("/", serveScalarHTML)
    mux.HandleFunc("/openapi.yaml", serveSpec)
    mux.HandleFunc("/scalar.js", serveScalarJS)
    return mux
}
```

Mounted in the server as:

```go
r.Group("/docs").Any("/*path", gin.WrapH(http.StripPrefix("/docs", docs.Handler())))
```

## Spec Source of Truth

The OpenAPI YAML at `docs/api/openapi.yaml` is the single source of truth. It is:

1. Hand-maintained alongside API changes (a PR that adds an endpoint must update the spec).
2. Validated in CI via `redocly lint` (or `spectral lint`) — broken specs fail the build.
3. Used to generate Go server stubs during development (optional, via `oapi-codegen`) but the generated code is not committed; handler implementations are hand-written.
4. Embedded into the binary for serving via Scalar.

## Public Documentation Hosting (Optional, Post-MVP)

In addition to the embedded docs, the project may publish a public version at `docs.leoflow.io` for marketing and onboarding. The CI workflow `docs.yaml` builds and deploys this from the same OpenAPI YAML. This is a v1.x concern, not MVP.

## Consequences

- The `internal/api/docs/` package owns the embedded Scalar assets. Updating Scalar means updating one file in that package.
- The OpenAPI YAML cannot drift from reality without triggering CI failures (lint check) and manual UI breakage in `/docs`.
- The `leoflow-server` binary grows by approximately 200-400 KB. Acceptable.
- The `/docs` route is unauthenticated by default (documentation should be public). For air-gapped or regulated deployments, a config flag `LEOFLOW_DOCS_REQUIRE_AUTH=true` gates it behind the standard JWT middleware.

## Alternatives Rejected

- **Swagger UI**: dated visual design, larger asset bundle, less polished code sample generation. The community is moving on.
- **Redoc**: lacks interactive request execution. Read-only docs do not help operators debug live API issues.
- **External hosting only (GitHub Pages / ReadMe / Mintlify)**: rejected because of version drift and air-gapped requirements.
- **No interactive docs, only Markdown reference**: rejected because the Airflow audience expects live API exploration.
