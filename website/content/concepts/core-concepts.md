---
title: Core concepts
linkTitle: Core concepts
weight: 20
description: How a DAG, its runs, and its tasks fit together — pause semantics, and why each DAG is its own container image.
---

Leoflow keeps Airflow's vocabulary so the UI and mental model are familiar. If you
just need the term definitions, see the [Glossary](/reference/glossary/); this page
covers how the pieces behave at runtime.

## What "paused" means (and what it does **not** mean)

A **paused** DAG is one whose `is_paused` flag is `true` in the metadata database
— typically toggled with the on/off switch next to the DAG name in the UI.

| Action | Paused DAG | Unpaused DAG |
|---|---|---|
| **Scheduler-created runs** (cron, `@daily`, `@hourly`, …) | ❌ Suspended — no new scheduled runs are created | ✅ Created at each interval |
| **Catchup backfills** | ❌ Suspended — backlog accrues silently until you unpause | ✅ Created on each tick (subject to `catchup`/`max_active_runs`) |
| **Manual triggers** (UI **Trigger DAG** button, `POST /api/v2/dags/{id}/dagRuns`) | ✅ **Always run** — pause does not gate manual triggers | ✅ Run |
| **In-flight runs** (already created when the pause flipped) | ✅ Continue to completion — pause is not a kill switch | ✅ Continue |

This is **intentional** and mirrors Apache Airflow's contract: *paused* gates the
scheduler, not the operator. An operator who triggers a DAG manually has already
acknowledged the side effects — pause is for *quieting the cron*, not for
*disabling the pipeline*.

If you want a real "no runs at all" switch, the supported pattern is the same as
in Airflow: pause the DAG **and** instruct operators not to trigger it. There is
no separate "disable" flag.

### Why not block manual triggers too?

Three reasons we kept Airflow's behavior:

1. **Operations parity.** Sites migrating from Airflow have runbooks ("trigger
   `etl_recover` manually if scheduler is paused") that depend on this.
2. **Debugging.** Pausing a flapping DAG and then manually triggering one good
   run to capture a clean trace is a common pattern.
3. **Escape hatch.** During an incident, the on-call may need to force a single
   run without unblocking the entire schedule.

If you triggered a paused DAG by accident, **delete the run** from the run list
— that's the supported undo. Pausing again afterward will not retroactively
suspend it.

## Why "DAG = image"

Airflow's pod-per-task model is right; its Python control plane is the bottleneck.
Leoflow keeps the model, rewrites the control plane in Go, and makes **each DAG its
own container image** — no shared `/dags` filesystem, no dependency hell. See
[ADR 0001](/project/adrs/0001-why-leoflow/) and
[ADR 0003](/project/adrs/0003-dag-as-image/).

See also: [Architecture](/concepts/architecture/) ·
[DAG authoring](/author-dags/dag-authoring/) · [Glossary](/reference/glossary/).
