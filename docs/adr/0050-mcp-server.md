# ADR 0050: Model Context Protocol (MCP) server

**Status:** Accepted — design ratified in a scoping review; implementation phased and **not started**.
**Date:** 2026-08-10
**Relates:** ADR 0008 (JWT auth), ADR 0024 (DAG parsing structural shim → `dag.json`), ADR 0040 (Airflow operator execution), ADR 0041 (build/push/register deploy), ADR 0048 (no user code in the control plane), ADR 0049 (split API/scheduler roles), ADR 0019 (secret encryption at rest)
**Issues:** #228 (per-tenant auth), #508 (tenant isolation is a caller convention, not a SQL predicate), #66 (restrict `MintUserToken`), #59 / #388 (least-privilege secret delivery), H4 / #537 (no token revocation)

## Context

Leoflow should expose a **Model Context Protocol** server so an LLM agent (Claude
Desktop, Claude Code, or any MCP client) can read and reason about DAGs, runs,
task instances, and logs — and, later, help author DAGs. The enterprise direction
makes MCP a headline surface, so it must be designed as a first-class, secure
component, not a bolt-on.

A prior scoping review produced a detailed recommendation set (patterned on the
`powerlab-mcp` work and a survey of the existing Airflow MCP servers). This ADR
records the decisions that survived that review, the places we deliberately
diverge from both the recommendation set and from `powerlab-mcp` (whose real
production scars inform several choices), and the structural prerequisites the
MCP depends on.

Three facts about Leoflow shape every decision below:

1. **`/api/v2` is Airflow-3.2-compatible** and is the same surface in both editions
   (Lite and Pro are one binary). An MCP that speaks only `/api/v2` is
   edition-agnostic and Airflow-compatible *by construction*.
2. **`dag.json` is a compiled, immutable artifact** produced by importing `dag.py`
   through the structural shim (ADR 0024), and it carries a digest-pinned image
   reference. It is not hand-authorable and not runnable without a built+pushed
   image.
3. **Auth is a single JWT plane** (issuer `leoflow`, HS256, audiences
   `leoflow-user` and `leoflow-agent`; claims carry tenant + roles), enforced by
   the API's `RequirePermission` middleware. There is no SSO/OIDC yet; it is on the
   enterprise roadmap.

## Decisions

### D1 — In-repo, not a separate repository
`cmd/leoflow-mcp` (binary) + `internal/mcp` (server core) live in the Leoflow
repo, versioned with the API they consume. A separate repo would create a
compatibility matrix while `/api/v2` is pre-1.0. This mirrors the `powerlab-mcp`
decision and its explicitly-deferred "standalone connector" idea. **Extraction to
a standalone "Go Airflow MCP" is revisited only when both signals appear:**
`/api/v2` has stabilized (~1.0) **and** there is demonstrated demand for a generic
Airflow MCP as an independent asset. If anything is ever extracted first, it is
`pkg/client` (see D8), not the MCP.

### D2 — HTTP `/api/v2`, not gRPC
The MCP talks to the control plane over HTTP `/api/v2` through a typed client
(D8), **never** gRPC and **never** by importing `internal/` service packages.
gRPC would mean building a new query service that duplicates `/api/v2`; it is
Go→Go request/response reads, so gRPC buys nothing and costs the
Airflow-compatible + edition-agnostic property. Leoflow's existing gRPC
(`agentrpc`) is the narrow in-pod agent surface (register / report / heartbeat /
xcom / logs) and is the wrong shape for querying DAGs and runs; the MCP does not
touch it. Revisit only if a future phase needs high-rate streaming (live log tail
across many tasks), and even then HTTP SSE likely suffices.

### D3 — Edition-agnostic; Lite/Pro are deployment profiles, not code paths
The MCP is parameterized by `(endpoint, transport, token, feature-flags)` and
**discovers backend capabilities at runtime** (a version/capability probe). It
never branches on "edition." "Leoflow-specific" is not "edition-specific": the
differentiators (policy over `dag.json`, tuned `diagnose_run`, the authoring
prompt) exist in both editions because it is one codebase. What varies between
Lite and Pro is only the deployment profile.

### D4 — Dual transport via the official SDK; stdio-first
One server core, two transports: **stdio** (`leoflow mcp`, for local Lite dev) and
**Streamable HTTP** (`POST /mcp`, the Pro service). The official
`modelcontextprotocol/go-sdk` (maintained with Google; **v1.0 stable with a
compatibility guarantee, currently v1.5**) provides stdio + SSE + Streamable HTTP
over the same tool/resource registration, so this is free, and its (experimental)
client-side OAuth is the SSO/OIDC federation path in D9. Two entry points share
`internal/mcp`: a **`leoflow mcp` subcommand** (stdio, bundled in the CLI for
local Lite dev) and a **`leoflow-mcp` binary** (the HTTP service for Pro). The MCP
is **optional**: it is never compiled into `leoflow-server`, and the Pro
Deployment ships **off by default** in the chart with a config kill-switch. A
single MCP process targets a single control plane (one `--server`); running
against multiple environments is multiple client-side MCP configs, **not**
environment routing inside the server (an agent must never pick the environment
as a parameter).

### D5 — Authoring generates `dag.py` + `leoflow.yaml`, never `dag.json`
The MCP writes the **human sources**, never the compiled artifact. `dag.json` is
produced by the Python shim importing `dag.py` (ADR 0024) and carries a
digest-pinned image that only exists after build+push (ADR 0041) — a hand-authored
`dag.json` is neither the source of truth, reviewable, nor registrable. The
authoring loop is: `scaffold_dag` writes sources → `leoflow compile` (local, no
push) produces `dag.json` → `validate_dag` checks schema + policy against that
`dag.json` → open a PR. **`validate_dag` validates structure, not runnability**:
only a real image build (CI, or `leoflow deploy` locally) proves a task can run —
the MCP must not claim otherwise. The authoring tools (`scaffold_dag`,
`validate_dag`, `open_dag_pr`) operate on a **local workspace** (they write files
and shell out to `leoflow compile`), so they exist **only on the stdio/`leoflow mcp`
transport**; the Pro HTTP service has no local `dag.py` and exposes read/diagnose,
not authoring.

### D6 — The MCP never deploys; the PR/CI is the gate
No MCP tool calls the register endpoint (`POST /api/v2/dags/{id}/versions`,
`write:dag`) or `leoflow deploy` directly. Authoring tools write files and open a
PR; the human + CI boundary does compile → build → push → digest-pin → register.
This matches the existing pipeline exactly and needs no new approval UI — Git is
the approval UI.

### D7 — Surface: Tools-first, three gated tiers
`powerlab-mcp`'s real chat-client testing found that clients under-render
Resources and Prompts and surface **Tools** prominently. So the MVP **leads with a
few high-value Tools** (`diagnose_run`, `search_logs`) and exposes Resources
(`dag://`, `run://`, `task://`, `log://`, `health://`) and the `dag_authoring`
Prompt as well, but does not bet the UX on them. Tools are gated in three tiers,
all **off by default**, and a disabled tool is **not registered** (never a tool
that exists and refuses):
- **read-only** (no gate): `diagnose_run`, `search_logs`, `search_docs`, `validate_dag`.
- **side-effect** (`EnableRunControl`, default false): `trigger_run`, `clear_task`.
- **destructive** (`EnableAuthoring`, default false): `scaffold_dag`, `open_dag_pr`.
- **forbidden** (never exist): direct `deploy`, `delete_dag`, `set_connection`.
Gating is by operator flag, not by edition (D3). Every listing paginates; every
log read is truncated by construction; responses are shaped, not upstream JSON
verbatim.

### D8 — `pkg/client`: the single typed `/api/v2` client (structural prerequisite)
Today there is no shared client — HTTP calls to `/api/v2` are hand-rolled ad-hoc
across ~8 CLI files, and there is no `pkg/`. The MCP must not become the 9th copy
and must not reach into `internal/`. So we **extract/generate `pkg/client`** (a
typed `/api/v2` client, generated from the OpenAPI, in the public `pkg/` path) and
route the MCP through it. The CLI's ad-hoc call sites migrate onto the same client
(deduplication + one contract; locked by the existing CLI tests). The generated
client carries its own types from the OpenAPI schemas, so `internal/domain` does
**not** move. Using the generated client (correct path-parameter encoding) also
closes a latent injection risk: the CLI currently string-concatenates
agent/user-supplied ids into URLs.

### D9 — Auth is the single JWT plane; no bespoke MCP auth
The MCP presents a **Leoflow JWT**, validated by the same `JWTAuth` +
`RequirePermission` + audit trail as any API client. The only addition is a third
audience, `leoflow-mcp`, of the **same** scheme (issuer/HS256/claims) — not a
parallel auth stack. Token issuance reuses the webserver flow (`/auth/token`
today; a `leoflow auth mcp-token` convenience is a thin wrapper, never a new
`MintUserToken` — that is the #66 bypass primitive). The MCP **passes through** the
caller's token; it never mints one. Tenant scope is enforced **upstream** by the
control plane, not filtered in the MCP. When SSO/OIDC lands for the webserver, the
HTTP-transport MCP federates to the same IdP via the SDK's OAuth support — the JWT
stays the internal session token.

### D10 — Untrusted-content threat model (leoflow-specific)
Anyone who can register a DAG controls text that enters an agent's context. Treat
as untrusted: `dag_id`, `description`, `owner`, tags, task names, all log content,
all XCom values, parser error messages. The MCP must delimit untrusted content in
an explicit envelope, sanitize it (strip control/ANSI bytes, cap line length),
truncate by construction, and re-validate any value read from a log/XCom before it
is used as a tool argument. This is the gap `powerlab-mcp`'s threat model did not
cover; here it is a first-class requirement, recorded in this ADR rather than left
implicit.

## Security posture

The separate-process design is the **more** secure choice, not a compromise: the
MCP accumulates no privilege of its own (no DB, no Redis, no K8s API), so a bug in
tool code can only do what the caller's token already authorizes — its blast
radius equals the caller's own API rights. Embedding the MCP in-process would put
tool logic inside the privileged control plane with direct datastore access; we
reject that.

Residual risks and their handling:
- **Token custody.** A pass-through proxy holds the user JWT transiently (worse in
  Pro HTTP, which holds many). Mitigate with short-lived tokens, the reduced-scope
  `leoflow-mcp` audience, TLS on both hops, never logging tokens, memory-only.
- **Upstream enforcement is only as strong as tenant isolation actually is.** D9's
  "trust the control plane to filter by tenant" inherits **#508** (tenant isolation
  is currently a caller convention, not a SQL predicate). In multi-tenant Pro this
  is a hard prerequisite; in single-tenant Lite it does not bite.
- **No revocation (H4 / #537).** A leaked token is valid until expiry. The MCP,
  being a token-holding proxy, raises the stakes, so revocation becomes a Pro
  prerequisite. It is shared webserver work — fixed once, both benefit.
- **Chattiness.** `/api/v2` is granular (designed for the UI); an agent flow like
  `diagnose_run` would otherwise be N round-trips. The fix is a **server-side
  aggregate endpoint** (joins done in the control plane), not gRPC.
- **Endpoint hygiene.** The MCP consumes only permission-gated `/api/v2` endpoints,
  never `/ui/` convenience routes; each endpoint's authz is audited before exposure.

## Prerequisites and phasing

**Auth is not adjusted first.** The Lite MVP (stdio, single-user, single-tenant)
runs on the current auth unchanged. The auth adjustments below are **Pro-phase
gates and shared webserver work**, sequenced before the Pro HTTP transport — not
before the MCP as a whole.

**Structural prerequisites (before/alongside the MVP):**
- **`pkg/client`** (D8) generated from the OpenAPI + CLI migrated onto it. This is the natural first implementation step (it need not be a standalone PR). A dry-run (`oapi-codegen` v2 against the current spec) confirmed feasibility: it generates a client that **compiles** cleanly (only `oapi-codegen/runtime` deps), 19 methods / 51 types over the specced surface. **But** the spec has no `operationId`s, so method names fall back to path-derived noise (`GetApiV2DagsDagIdDagRuns`) — so a prerequisite of the `pkg/client` work is **adding `operationId` to every operation** for clean, stable method names.
- **Extend `/api/v2` + OpenAPI** for the differentiators the spec does not yet cover — audited to be ~10 of 108 routes; `dagVersions`, the `dag.json` spec/source, and `/monitor` are absent. Needed for authoring/diagnose, **not** for the read-only Airflow-compat MVP. Add these with `operationId`s from the start.
- **Compatibility probe (validate-before):** point an existing OpenAPI-driven Airflow MCP at `leoflow lite`; what works confirms the Airflow-compat surface end-to-end, what it cannot see is the differentiator gap.

**Phasing:**
1. **MVP — Lite, stdio, read + authoring.** `pkg/client`, `internal/mcp` + `cmd/leoflow-mcp`, read resources + `diagnose_run` + `search_logs`, `dag_authoring` prompt + `scaffold_dag`/`validate_dag` (local compile, no deploy). No auth change. The Lite subprocess executor lets a dev run the DAG locally, so the loop write → validate → run → diagnose is complete.
2. **Extend OpenAPI** for the differentiators, generate the corresponding `pkg/client` surface.
3. **Pro — HTTP transport + auth.** `POST /mcp`, JWT pass-through, `leoflow-mcp` audience. **Gates:** #228 (per-tenant auth), #508 (tenant isolation), H4 (revocation) — shared with the enterprise-auth roadmap.
4. **Run control** (`EnableRunControl`) after Pro is validated on a real cluster.

## `powerlab-mcp` lessons applied as requirements
- Use the **official** Go SDK from day one (powerlab shipped on a third-party SDK and paid to migrate). — **decided:** `modelcontextprotocol/go-sdk`, v1.x stable.
- **Tools-first** surface (clients under-render Resources/Prompts). — D7.
- **Identity propagation on day zero** (powerlab deferred it and its audit logs show `loopback` instead of the user). — D9.
- **No loopback auth skip** (invalid where user code runs in the cluster). — D9.
- **Untrusted-content threat model written down**, not implicit. — D10.
- **Structured degradation**, not raw errors (powerlab leaked subprocess errors). — a control-plane-unavailable response has a recognizable shape.
- **A real agent-conversation test per tool**, not only a smoke client — smoke is necessary but not sufficient (three silent bugs in a powerlab batch were caught only this way).

## Consequences
- One binary serves local Lite dev, Lite-as-prod, and Pro, differing only by config; no per-edition MCP.
- The MCP's correctness is bounded by `/api/v2`; extending agent capability means extending the API + OpenAPI, which also benefits the CLI and any other client.
- The read-only Lite MVP ships without any auth, tenant-isolation, or revocation work; those become explicit Pro gates.
- Authoring produces reviewable Git diffs of `dag.py`/`leoflow.yaml`; deploy stays with CI.

## Open questions
1. **`leoflow-mcp` scope enforcement:** RBAC today is role-based, not scope-based. A reduced-scope MCP token needs either a scope claim the middleware honors (new) or reliance on the operator flags (D7) + roles for the MVP. The MVP takes the latter; a true scoped audience is later hardening.
2. **Where `dag_authoring` reads the active policy rules** (embedded, from the control plane, or the team repo) — decided when the policy engine lands (a separate feature, not part of this ADR).

## Resolved during review
- **SDK: `modelcontextprotocol/go-sdk`** (official, maintained with Google; v1.0 stable with a compatibility guarantee, currently v1.5; stdio + SSE + Streamable HTTP; client-side OAuth is experimental). Confirmed current as of 2026-08, so no spike is needed — this supersedes the earlier "third-party SDK" path `powerlab-mcp` took.
