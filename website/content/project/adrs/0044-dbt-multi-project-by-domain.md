---
title: "ADR 0044: dbt multi-project — one project per business domain"
linkTitle: 0044 · dbt multi-project — one project per business domain
weight: 440
description: "ADR 0044: dbt multi-project — one project per business domain"
---

**Status:** Accepted — multi-group expansion validated live (sales + marketing in one DAG)
**Date:** 2026-07-03
**Companions:** ADR 0042 (dbt support), ADR 0043 (TaskGroup split/fused execution), ADR 0024 (parser shim), the editions split (Lite/Pro)

## Context

A single monolithic dbt project does not scale once several teams and business
subjects share it: one CI, one ownership boundary, one ever-growing manifest, and a
merge-conflict funnel. The industry answer — dbt Labs' **dbt Mesh** — is to split a
warehouse's transformations into **domain-oriented projects**: one per business
subject (sales, marketing, finance), each with its own ownership, CI, and lineage.

Leoflow already admits this at the schema level: `dbt_groups` in `leoflow.yaml` is a
**map**, so a `dag.py` can embed more than one named group, and separate DAGs trivially
carry separate projects. This ADR records that **multiple dbt projects, one per
domain, is a first-class supported pattern** — its guarantees, its best practices, and
its limits — so the shape is intentional rather than incidental.

## Decision

Leoflow supports **multiple dbt projects, one per business domain**, in two shapes:

1. **Multiple dbt groups in one DAG.** `dbt_group("sales")` and `dbt_group("marketing")`
   in one `dag.py`, each configured under `dbt_groups.<name>` pointing at its own
   project directory. The compiler expands each group and **namespaces** its tasks as
   `<group>__<node>` (e.g. `sales__stg`, `marketing__orders`), so groups never collide
   and can be wired around shared operators.

2. **Multiple DAGs, one project each.** Each domain DAG compiles independently — the
   natural boundary when domains schedule, own, and deploy separately.

```yaml
# biz/leoflow.yaml
dag_id: biz
dbt_groups:
  sales:     { project: ./sales,     connection: warehouse, granularity: node }
  marketing: { project: ./marketing, connection: warehouse, granularity: node }
```

```python
# biz/dag.py — two domains, mixed with operators
with DAG("biz", schedule="@daily"):
    extract = PythonOperator(task_id="extract", python_callable=pull)
    extract >> dbt_group("sales")
    extract >> dbt_group("marketing")
```

## Guarantees (validated)

- **Namespacing.** `<group>__<node>` — no cross-group `task_id` collision.
- **Independence.** Each group has its own `dbt_project.yml`, manifest, and profile
  (connection); one domain's compile never touches another's.
- **Mixes with operators (ADR 0043).** A group's roots depend on its upstream
  operators; downstream operators depend on the group's leaves.
- **Validated live.** `sales` + `marketing` in one DAG compiled to
  `start`, `sales__raw`, `sales__stg`, `marketing__raw`, `marketing__stg` — both
  groups expanded, namespaced, and wired.

## Best practices — organizing dbt by domain

- **One project per domain** (sales, marketing, finance): a real ownership boundary, a
  smaller mental model, independent CI, and clearer lineage than a monolith.
- **Group name = domain.** It becomes the task prefix (`sales__orders`), so the graph
  reads as lineage at a glance.
- **One DAG per domain** when domains schedule/own/deploy independently — the common
  case. **Multiple groups in one DAG** when domains share a run cadence or an
  orchestration seam (a shared `extract` upstream, a shared `publish` downstream).
- **Keep projects independent.** Share across domains through the **warehouse** (a
  domain reads another's *published* tables by name), not through cross-project dbt
  `ref()` (see limits).
- **Config in `leoflow.yaml`, not `dag.py`** (ADR 0043): packing (Lite vs Pro), the
  connection, and granularity change per environment without editing Python.
- **A connection per domain.** Each group may target its own warehouse/connection.

## Limits

- **No cross-project `ref()` (full dbt Mesh).** Each group is an isolated project with
  its own manifest; Leoflow does not resolve a model in one project referencing a model
  in another. Cross-domain dependencies go through the warehouse (published tables) or
  explicit task edges — not dbt refs. A future dbt-mesh cross-ref resolution
  (multi-manifest, version pinning) is out of scope here.
- **Node-name uniqueness within a project** (issue #398): cross-package duplicate node
  names need `unique_id` namespacing; today `task_id`s must be unique within a group.

## Consequences

- Teams own domain projects independently and compose them at the DAG layer, mixed
  with operators and other domains.
- The `dag.json` stays a flat, namespaced task graph — the Go scheduler needs no dbt or
  mesh awareness; multi-project is entirely a compile-time expansion.
- Cross-domain lineage is expressed at the warehouse/table level, not the dbt-ref
  level — a deliberate simplification that keeps projects decoupled.

## Alternatives rejected

- **Single monolithic dbt project.** Does not scale to multiple teams/domains: one CI,
  one ownership, one huge manifest, a merge-conflict funnel.
- **Full dbt Mesh with cross-project refs now.** Powerful but heavy (multi-manifest
  resolution, cross-project version pinning); deferred until there is real demand — the
  warehouse-level sharing above covers the common case.
