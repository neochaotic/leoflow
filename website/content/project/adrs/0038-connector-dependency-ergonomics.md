---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0038-connector-dependency-ergonomics.html
# --- end AUTO redirect aliases ---
title: "ADR 0038: Connector dependency ergonomics — `connectors:` sugar + `dependencies:` escape hatch"
linkTitle: "0038 · Connector dependency ergonomics — `connectors:` sugar + `dependencies:` escape hatch"
weight: 380
description: "ADR 0038: Connector dependency ergonomics — `connectors:` sugar + `dependencies:` escape hatch"
---

**Status:** Accepted
**Date:** 2026-06-03
**Companions:** ADR 0036 (Airflow runtime compat shim — deferred; the task image ships the real Airflow Task SDK), ADR 0024 (parser structural shim), ADR 0014 (admin panel is Go-only)

## Context

With ADR 0036's runtime shim **deferred**, the task image ships the real
`apache-airflow-task-sdk` (it pulls `apache-airflow-core`), under which provider
hooks already work — the env-secrets backend reads `AIRFLOW_CONN_*`. To use an
Airflow connector, a user's DAG does:

```python
from airflow.providers.postgres.hooks.postgres import PostgresHook
hook = PostgresHook(postgres_conn_id="pg_target")
hook.get_records("SELECT 1")
```

…and declares the provider in `leoflow.yaml`. Because the install is **normal**
(not `--no-deps`), the provider pulls its own driver, so the minimum is one line:

```yaml
dependencies:
  - apache-airflow-providers-postgres   # pip also pulls psycopg2-binary, common-sql, …
```

Two frictions remain: (1) the user must know the **exact package name**
(`apache-airflow-providers-postgres`, and that `amazon.aws` → `…-amazon` but
`microsoft.mssql` → `…-microsoft-mssql`), and (2) forgetting it yields a cryptic
`ModuleNotFoundError` at **task runtime**, after the DAG was already dispatched.

Leoflow already maintains a curated connection-type catalog
(`internal/api/connection_hook_meta.go` — `conn_type → hook class`), so the
mapping `conn_type → pip package` is a small, already-half-built table.

## Decision

1. **Sugar field `connectors:` in `leoflow.yaml`.** Short connection-type names
   expand, at compile, into provider packages via the curated catalog:

   ```yaml
   # the easy path — for the common connectors
   connectors:
     - postgres        # → apache-airflow-providers-postgres (driver auto-pulled)
     - http
   ```

   The expansion is logged (`postgres → apache-airflow-providers-postgres`) so it
   is never opaque.

2. **`dependencies:` stays as the escape hatch — for advanced users.** Arbitrary
   packages, **pinned** versions, and any connector not in the catalog:

   ```yaml
   dependencies:
     - apache-airflow-providers-postgres>=6.0,<7   # pin
     - apache-airflow-providers-someobscure        # not in the catalog
     - duckdb==1.1.3                               # any lib
   ```

   The user picks whichever style they prefer; the two coexist. Effective install
   set = expand(`connectors`) ∪ `dependencies`.

3. **The catalog is the single source of truth** (`conn_type → pip package`),
   shared by three consumers: the `connectors:` expansion, the admin-panel /
   connection-probe nudge, and the compile-time validation below.

4. **Clear, actionable errors at compile (not at runtime):**
   - An unknown value in `connectors:` fails compile: *"unknown connector 'postgres2';
     known: postgres, mysql, …; or add the pip package to `dependencies:`."*
   - A `dag.py` that imports `from airflow.providers.<X>.hooks…` whose provider is
     in **neither** `connectors:` nor `dependencies:` fails compile with the exact
     line to add: *"PostgresHook needs `apache-airflow-providers-postgres` — add it
     to `connectors: [postgres]` or `dependencies:`."*

## Consequences

- **Better UX, lower error rate.** The common case is `connectors: [postgres]`;
  the tool maps the package and the driver comes free. The advanced case keeps
  full control (pinning, arbitrary packages). Missing deps fail **loud and early**
  (compile) with the fix in the message, not as a cryptic runtime import error.
- **A product differentiator.** "Declare `connectors: [postgres]` and you're done"
  is a cleaner story than Airflow's "find the right provider package and driver
  yourself."
- **Maintenance:** the catalog must track Airflow's provider package names — but
  it is already maintained for the admin form (15 common types); anything outside
  it falls back to `dependencies:`, so coverage is never a hard wall.
- **No version pinning in the sugar** (by design — `connectors: [postgres]` carries
  no version). Users who must pin use `dependencies:`. A future `postgres@6.0`
  shorthand can be added without breaking the bare form.
- **Cross-cutting source of truth:** the catalog now carries the pip package, so
  the parser/compiler (Go + Python) and the API all read one place; drift between
  the form, the sugar, and the validator is structurally prevented.
