# ADR 0036: Airflow 3.X runtime compatibility shim — one model, one policy seam

**Status:** Accepted
**Date:** 2026-06-03
**Companions:** ADR 0014 (no provider hooks in core), ADR 0019 (encryption at rest), ADR 0021 (`AIRFLOW_CONN_*` wire format), ADR 0024 (parser structural shim), ADR 0035 (cloud connector auth — keyless-first)

## Context

Existing Airflow 3.X DAGs commonly use provider hooks — `from airflow.providers.postgres.hooks.postgres import PostgresHook`, `from airflow.providers.google.cloud.hooks.gcs import GCSHook`. Leoflow today injects `AIRFLOW_CONN_*` env vars (ADR 0021) but offers no `BaseHook.get_connection()` surface, so user code has to parse the URI manually. This forces an unnecessary rewrite of every migrating DAG.

Two compatibility paths were evaluated (`docs/planning/airflow-connector-compatibility.md`):

- Re-implement hooks natively in Leoflow (~4,000 LOC; breaks every `from airflow.providers...` import).
- Pull `apache-airflow` itself into task images (adds 200-300 MB, 600 transitive deps, 4-7 s Lite cold start, imports a DB stack into pods that have no DB).

Neither serves both goals — Airflow-compat for adoption *and* long-term independence.

A **minimal runtime shim** of the Airflow 3.X SDK surface (`airflow.sdk.definitions.{hooks.base, connection, variable}` + `airflow.providers.common.compat.*` re-exports + a vendored `DbApiHook` under Apache 2.0) is ~1,200 LOC of pure Python. Leoflow's `Connection` model already has 100% field-level parity with Airflow 3.X's. Strategy A in the planning doc.

Separately, ADR 0035 (GCP keyless-first) introduced a security invariant: cloud keys should not enter Leoflow's DB. Naive Strategy A would let an upstream `GCSHook` accept `keyfile_dict` from the Connection without warning and would silently drop `key_secret_name` (a Leoflow-only field), contradicting ADR 0035.

The two intents meet at the `BaseHook.get_connection` seam.

## The two halves never live together

Connection **metadata** and connector **code** are owned by different sides of the
system. They meet only at runtime, via an env var. The full picture:

```text
┌─────────────────────────────────────────┐    ┌──────────────────────────────────┐
│  Admin UI / API  (Leoflow control plane,│    │  leoflow.yaml  (user, per-DAG)   │
│                   Go — ADR 0014)        │    │                                  │
│                                         │    │  dependencies:                   │
│  POST /api/v2/connections               │    │    - apache-airflow-providers-   │
│  {conn_id: "my_pg", type: "postgres",   │    │      postgres==6.0               │
│   host: "...", login: "...",            │    │    - psycopg2-binary==2.9        │
│   password: "...", extra: "{...}"}      │    │                                  │
└────────────────────┬────────────────────┘    └─────────────────┬────────────────┘
                     │                                            │
                     ▼                                            ▼
┌─────────────────────────────────────────┐    ┌──────────────────────────────────┐
│  Leoflow DB (encrypted, ADR 0019)       │    │  DAG image (built once per push) │
│  connections:                           │    │  ─────────────                   │
│    id="my_pg" type="postgres"           │    │  leoflow-base:py3.11             │
│    password = AES-256-GCM(...)          │    │  + apache-airflow-providers-     │
│    extra    = AES-256-GCM(...)          │    │      postgres   (PostgresHook)   │
└────────────────────┬────────────────────┘    │  + leoflow-runtime-compat-shim   │
                     │                          │      (airflow.sdk.* shim,        │
                     │ on dispatch              │       ADR 0036)                  │
                     ▼                          └─────────────────┬────────────────┘
┌─────────────────────────────────────────┐                      │
│  Leoflow agent (Go, in the pod or       │                      │
│  subprocess host)                       │                      │
│  - decrypts password + extra            │                      │
│  - renders the URI:                     │                      │
│    AIRFLOW_CONN_MY_PG=                  │                      │
│      postgres://login:pw@host:5432/db   │                      │
│      ?__extra__={"sslmode": ...}        │                      │
│    (ADR 0021 wire format)               │                      │
│  - injects env var into the task        │                      │
└────────────────────┬────────────────────┘                      │
                     │                                            │
                     └────────────────► task process ◄────────────┘
                                              │
                                              ▼
                ┌──────────────────────────────────────────────┐
                │  User DAG (Python)                           │
                │                                              │
                │  from airflow.providers.postgres.hooks       │
                │       .postgres import PostgresHook          │
                │  hook = PostgresHook(postgres_conn_id=        │
                │                      "my_pg")                │
                │  hook.get_records("SELECT 1")                │
                └─────────────────────┬────────────────────────┘
                                      │
                                      ▼ provider calls
                                      │ BaseHook.get_connection("my_pg")
                ┌──────────────────────────────────────────────┐
                │  Leoflow runtime compat shim  (ADR 0036)     │
                │  ──────────────────────────                  │
                │  1. Read AIRFLOW_CONN_MY_PG env var          │
                │  2. Parse URI → host/login/password/port/    │
                │      schema/extra                            │
                │  3. Pre-processor (per-type policy seam)     │
                │     • cloud type → cloud resolver            │
                │       (ADR 0035 chain: keyfile_dict →        │
                │        key_path → key_secret_name → ADC)     │
                │       fetches Secret Manager NOW if          │
                │       key_secret_name is set; emits the      │
                │       fetched key as a transient keyfile_    │
                │       dict so the upstream hook understands. │
                │     • non-cloud type → pass through.         │
                │  4. Return a canonical                       │
                │      airflow.sdk.definitions.Connection      │
                └─────────────────────┬────────────────────────┘
                                      │
                                      ▼
                ┌──────────────────────────────────────────────┐
                │  Upstream PostgresHook                       │
                │  (apache-airflow-providers-postgres)         │
                │  - receives the canonical Connection         │
                │  - psycopg2.connect(...) → real query        │
                └──────────────────────────────────────────────┘
```

Two consequences fall out of the picture and matter for this ADR:

- Leoflow's image **never** carries provider code. `apache-airflow-providers-*` is opt-in per DAG via `leoflow.yaml.dependencies`. Multiple DAG images can declare different providers and still share the same admin-managed Connection — connections are BYO-hook.
- The pre-processor is the **only** seam where ADR 0035 lives. There is no parallel "native" connector hierarchy; the security policy is policy-as-code at one point.

## Decision

1. **Build one runtime shim, on by default.** A new `leoflow_python_compat/airflow/...` package mirrors Airflow 3.X's `airflow.sdk.*` surface (`BaseHook`, `Connection`, `Variable`, exceptions, `execution_time.context`, the `providers.common.compat.*` re-exports). The shim ships in every Leoflow runtime image (~50 KB, dormant until a provider import triggers it). Upstream provider packages (`apache-airflow-providers-<X>`) are **opt-in** — declared in `leoflow.yaml.dependencies` and pip-installed during DAG image build. The Leoflow control plane image **never** carries provider code.

2. **Vendor `apache-airflow-providers-common-sql.DbApiHook` verbatim** under Apache 2.0 attribution. That single 1,250-LOC file lights up postgres, mysql, sqlite, mssql, snowflake, oracle, trino, db2, vertica, exasol without re-implementation. Pin the vendored version per Leoflow release.

3. **No `apache-airflow` install at runtime.** The shim satisfies every `from airflow.sdk...` and `from airflow.providers.common.compat...` import the providers actually use. Upstream provider wheels the user pip-installs resolve their `airflow` imports against the shim.

4. **One pre-processor enforces ADR 0035 at the seam.** `BaseHook.get_connection(conn_id)` reads `AIRFLOW_CONN_<ID>` (the agent already emits this — `internal/agent/runner.go:170`), then runs `extra` through a **policy pipeline before returning** the Connection to any upstream hook:
   - **Cloud-typed connections** route through a per-cloud resolver (`gcp_resolver.py`, future `aws_resolver.py`, `azure_resolver.py`). Each follows the ADR 0035 chain: `keyfile_dict` → emit + log a one-time warning ("ADR 0035: cloud key stored in Leoflow DB; prefer `key_path` or Workload Identity"); `key_path` → pass through (upstream hooks already understand it); `key_secret_name` → **fetch the secret now**, emit it as a transient `keyfile_dict` for the upstream hook's call site only — the secret never lands in our DB; empty → leave minimal, upstream falls through to ADC / Workload Identity.
   - **Non-cloud connections** (postgres, mysql, http, redis, etc.) pass through unchanged — the user/password model in ADR 0019 already governs them.

5. **Skip the Airflow SecretsBackend chain.** The shim's `_get_connection` reads `AIRFLOW_CONN_*` directly. Leoflow's connection delivery is single-source; mirroring Airflow's backend-chain plumbing would be code without behavior we don't already have.

6. **CI matrix is the regression gate.** Every Leoflow minor release pip-installs upstream `apache-airflow-providers-{postgres,sqlite,redis,http,mysql}` against the shim and runs the providers' own smoke tests. Each new Airflow minor gets a budgeted shim-alignment pass (~1-2 engineer-days per minor).

7. **Officially-supported hooks for Leoflow `v0.x`:** `postgres`, `http`, `sqlite`, `redis`, `mysql`. Each ships with a cookbook page, a connection-test DAG, and CI gating. Cloud hooks (`gcp_*`, future `aws_*`, `azure_*`) add an entry per phase, gated by the per-cloud resolver from clause 4.

8. **`leoflow_runtime` native API is an additive overlay, not a replacement.** For each officially-supported hook we may later ship a Leoflow-flavored surface (e.g. `from leoflow_runtime.cloud.gcp import gcs_client`) that delegates through the **same** pre-processor. No second hook hierarchy; ergonomic surface only. Deferred until the shim is in production for two releases.

9. **Where connector code lives — the BYO-hook contract (the dependency is mandatory).**
   - **Connection metadata** (host, login, password, extra JSON) is owned by the Leoflow control plane: created in the admin UI, encrypted at rest (ADR 0019), delivered to the task as `AIRFLOW_CONN_<ID>` (ADR 0021).
   - **Hook code** (e.g. `PostgresHook.get_records()`, `GCSHook.upload()`) is owned by the user's DAG image: pip-installed from `apache-airflow-providers-<X>` per `leoflow.yaml.dependencies`. **Leoflow ships no provider code.** The shim provides only the `airflow.sdk.*` import surface; without the matching provider wheel installed, `from airflow.providers.postgres.hooks.postgres import PostgresHook` raises `ModuleNotFoundError` — a clear, fast, import-time failure.
   - They meet exclusively at the env var. Different DAG images can declare different providers and still share the same admin-managed connection.
   - **Doc contract (mandatory).** Every cookbook page for an Airflow-compat hook lists the required `dependencies:` entry in a fixed format at the top of the page:
     ```yaml
     # leoflow.yaml — required for PostgresHook
     dependencies:
       - apache-airflow-providers-postgres>=6.0
       - psycopg2-binary>=2.9
     ```
     The cookbook page also names the exact `ModuleNotFoundError` the user will see if the dep is missing, so search engines route them back. The shim emits a one-time helpful note at runtime if `from airflow.providers...` fails for a `conn_type` whose metadata is registered in the admin UI but whose pip package is not installed — pointing at the corresponding cookbook page.
   - **No default provider bundling.** Leoflow's `leoflow init` scaffold leaves `dependencies: []`. We do not auto-add providers ("opinionated defaults" would carry image size + CVE surface the user didn't ask for). The first time the user adds a Connection in the admin UI, the UI shows a one-line note: "Don't forget to add `apache-airflow-providers-<type>` to your DAG's `leoflow.yaml.dependencies`."

10. **Admin panel implementation stays Go-only.** Connection CRUD, type catalog, form-field schema, structural validation, and UI rendering live in the Go control plane (no Python, no Airflow SDK call — ADR 0014). Two intentional divergences from Airflow's admin-side pattern:
    - **Form widgets** — `internal/api/connection_hook_meta.go` holds a curated static registry for the ~10 most-used types (postgres, mysql, sqlite, mssql, redis, http, gcp, future aws/azure, plus a generic fallback). Non-curated types render with a generic form (standard fields + raw "Extra JSON" textarea). Hook functionality at runtime is identical either way; the divergence is only in form polish. Tradeoff named: when an upstream provider adds a new field, we update the Go registry on the next release rather than introspecting Python at request time.
    - **`test_connection()`** — defaults to structural validation only (`internal/api/connection_probe.go`). A real "probe in an ephemeral pod" path is reserved as an opt-in Pro feature in a follow-up ADR (would spin up a one-shot pod with the user's image, invoke `Hook.test_connection()`, return the result, tear down).

11. **Operators we already ship are unchanged — separate parser shim, separate path, no provider deps required.** The existing parser-side Airflow shim (ADR 0024 — `parser/leoflow_parser/_shim/airflow/` with `DAG`, `BaseOperator`, `XComArg`, `PythonOperator`, `EmptyOperator`, `BashOperator` (stub), `HttpOperator` (stub), and the `@task` decorator) is **structural only and lives at compile time**. The parser converts those into Leoflow task types (`bash`, `python`, `http_api`) in `dag.json`, and `runtime/python/leoflow_runtime/runner.py` executes them directly — never importing `airflow.providers.*`, never touching `BaseHook`.

    Two tiers of "Airflow imports that work on Leoflow" — they have very different dependency contracts and must be documented as such:

    | Tier | Import | Needs `apache-airflow-providers-*` in `leoflow.yaml.dependencies`? | Runtime path |
    |---|---|---|---|
    | **A. Native (already shipped)** | `from airflow.sdk import DAG, task` | **No** | Parser maps to `python` / `bash` / `http_api` task types; runtime executes directly. |
    | A. | `from airflow.providers.standard.operators.python import PythonOperator` | **No** | Same as above. |
    | A. | `from airflow.providers.standard.operators.bash import BashOperator` | **No** | Same. |
    | A. | `from airflow.providers.standard.operators.empty import EmptyOperator` | **No** | Same. |
    | A. | `from airflow.providers.http.operators.http import HttpOperator` | **No** | Task type `http_api` — Leoflow agent executes the HTTP call. |
    | **B. Compat (this ADR)** | `from airflow.providers.postgres.hooks.postgres import PostgresHook` | **Yes** — `apache-airflow-providers-postgres` + `psycopg2-binary` | Through the runtime compat shim (clauses 1-9). |
    | B. | `from airflow.providers.google.cloud.hooks.gcs import GCSHook` | **Yes** — `apache-airflow-providers-google` | Through the shim + the GCP resolver (clause 4 + ADR 0035). |
    | B. | `from airflow.providers.http.hooks.http import HttpHook` | **Yes** — `apache-airflow-providers-http` | Through the shim. (Distinct from `HttpOperator` in tier A.) |
    | B. | any other `from airflow.providers.<X>.hooks.<Y>...` | **Yes** — the matching `apache-airflow-providers-<X>` | Through the shim. |

    **Out of scope (still rejected at compile time per the closed-set policy):** sensors, dynamic task mapping (`.expand`/`.partial`), `TaskGroup`, branching operators, untyped operators outside the standard / http providers. Parser fails fast with "not supported by Leoflow" — no behavior change in this ADR.
    - **The runtime compat shim of this ADR (0036) is a parallel new module, not a rewrite of the parser shim.** The two coexist:
      - **Compile time (ADR 0024):** parser shim resolves `from airflow.sdk import DAG, task`, `from airflow.providers.standard.operators.bash import BashOperator`, etc. so the parser can introspect `dag.py`. Result: `dag.json` with task entries of type `bash`/`python`/`http_api`.
      - **Runtime (ADR 0036):** compat shim resolves `from airflow.providers.<X>.hooks.<Y> import <Z>Hook` so user code can fetch a `Connection` and call provider methods. Triggered only when the DAG actually imports a hook.
    - **Existing DAGs that use only `@task`, `BashOperator`, `PythonOperator`, `HttpOperator`, `EmptyOperator` are completely unaffected.** They never touch the runtime compat shim and require no `apache-airflow-providers-*` in `leoflow.yaml.dependencies`.
    - **A DAG that mixes both** (e.g. uses `@task` for the work AND `PostgresHook` inside the callable to talk to a DB) gets the existing operator behavior **plus** the runtime compat shim — both apply, no conflict. The DAG declares the provider for the hook and otherwise looks the same.

12. **Supported Airflow line — pin to a minor, not a patch.** Leoflow already targets Airflow 3.2.x for the HTTP API and UI compatibility (CLAUDE.md non-negotiable #8). The runtime compat shim mirrors that:

    - **Compat target for Leoflow `v0.x`:** the Airflow **3.2** minor line (currently `3.2.1` at the time of this ADR; whatever the latest stable patch is when each Leoflow release ships). Providers tested in CI are the ones declaring compatibility with `apache-airflow~=3.2` (i.e. `>=3.2.0,<3.3.0` in pip semantics).
    - **Why a minor, not a patch.** Patch bumps inside 3.2.x are bug-fix-only by Airflow's policy; the SDK surface is stable across patches. Pinning to `3.2.1` would force a Leoflow rebump on every Airflow patch with no behavior change in our shim. Pinning to the minor line lets us track Airflow's own stable contract.
    - **Concrete pins per layer:**
      - `leoflow_python_compat/airflow/__version__` reports `"3.2"` (the minor we mirror), not a specific patch.
      - The CI matrix's reference Airflow install is the latest 3.2.x patch at job run time, refreshed on each Leoflow release cut.
      - Cookbook pages for hooks pin provider lines using the upstream's compatibility matrix (e.g. "Postgres: `apache-airflow-providers-postgres>=6.0,<7` — built against Airflow 3.2.x"). We do not pin to a specific provider patch; that's the user's choice via their `leoflow.yaml.dependencies`.
    - **When Airflow 3.3 ships,** a follow-up Leoflow release bumps the target to 3.3.x in a single coordinated change: shim alignment pass (budgeted 1-2 engineer-days per clause 6), CI matrix rebuild, release notes name the supported Airflow line explicitly. Older Leoflow versions stay pinned to 3.2.x and receive security patches but not new-Airflow compat.
    - **Documented support matrix.** A table in the chart / install docs maps each Leoflow release to the Airflow minor line it targets, e.g.:
      ```text
      Leoflow 0.1.x  →  Airflow 3.2.x  (current)
      Leoflow 0.2.x  →  Airflow 3.2.x  (continues)
      Leoflow 0.3.x  →  Airflow 3.3.x  (when upstream cuts 3.3)
      ```
      Mismatches (a DAG image with a provider that requires Airflow 3.3 running on Leoflow 0.2) fail at task import with the upstream's own version-check error — we do not add a second version check.

## Consequences

- **Drop-in compatibility today.** Existing Airflow 3.X DAGs using `from airflow.providers.<X>.hooks.<Y> import <Z>Hook` run unchanged on Leoflow once their `leoflow.yaml.dependencies` lists the corresponding provider. No DAG-side rewrite.
- **ADR 0035 honored.** Cloud keys never reach upstream hooks via Leoflow's DB unless the operator explicitly chose `keyfile_dict`. `key_secret_name` (Secret Manager) becomes a first-class path that flows through the pre-processor without polluting the DB.
- **Independence preserved.** No `apache-airflow` install. Lite cold start unchanged. Task pod image bumps by ~50 KB on the shim itself (versus +200-300 MB if we let `apache-airflow` get pulled in).
- **Bounded maintenance.** ~1,200 LOC of shim + ~30 LOC per cloud resolver. Airflow minor drift budgeted at 1-2 engineer-days per release, gated by the CI matrix.
- **One model, two faces.** Compat surface = `from airflow.providers...` (the present); native overlay = `from leoflow_runtime...` (the future). Same wire identity, same security stance, no parallel registry.
- **Admin UI stays Go.** No Python in the control plane; no Airflow SDK calls. Curated registry for the form polish; structural validation today, opt-in real probe later.
- **Lite vs Pro is unchanged.** Pre-processor logic is pure Python; runs identically under subprocess (Lite) and pod (Pro). The cloud resolvers' "what counts as keyless" differs per edition (Lite uses host ADC; Pro uses Workload Identity) — already true and respected via `google.auth.default()` semantics.

## Alternatives considered

- **Re-implement hooks natively in `leoflow_runtime.hooks.*` (~4,000 LOC).** Rejected as the primary path: breaks every Airflow DAG's `from airflow.providers...` imports unless we also ship an import rewriter, and matching upstream method signatures eats most of the LOC savings. Viable later as a targeted ergonomic overlay (clause 8); not viable as the only surface.
- **Allow user-installed `apache-airflow` runtime.** Rejected: +200-300 MB image bloat + 600 transitive Python deps + 4-7 s cold start + a DB stack imported into pods that have no DB + CVEs we don't audit. Conflicts with the "Python minimal, Go max" principle. Acceptable only as a user-managed escape hatch for connectors the shim doesn't support.
- **Two parallel connector tiers (Leoflow-native vs Airflow-compat).** Rejected: forces the user to pick a tier up-front, breaks the drop-in promise, and doubles the catalog / UI / probe code. The pre-processor seam achieves the same security stance with one model.
- **Move ADR 0035 enforcement into the Go control plane.** Rejected: would re-introduce cloud SDKs in core (violates ADR 0014). Resolution at the task is where the token exchange already happens correctly per ADR 0035 clause 5.
- **Python in the admin panel (introspect provider form widgets at request time).** Rejected: pulls Airflow + every installed provider into the control plane image; conflicts with ADR 0014. Curated Go registry + generic fallback is the right tradeoff for form polish; runtime behavior is identical.
