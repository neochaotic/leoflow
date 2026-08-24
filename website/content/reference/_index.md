---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /reference.html
# --- end AUTO redirect aliases ---
title: Reference
linkTitle: Reference
weight: 60
description: References for every Leoflow surface — the HTTP API, CLI, Go packages, Python runtime, configuration, and MCP server.
cascade: { type: docs }
menu:
  main:
    weight: 60
---

References for every Leoflow surface. The HTTP API, CLI, Go, and Python references
are **generated from source on every push**, so they never drift from the code. The
[Configuration](/reference/configuration/) page is hand-maintained against
`internal/config` — treat the server source as the final authority.

{{% cardpane %}}
{{% card header="**HTTP API (Scalar)**" %}}
The `/api/v2/` control-plane API — Airflow 3.2.x-compatible — as an interactive
Scalar reference.

[Open the API reference →](/reference/api/)
{{% /card %}}

{{% card header="**CLI reference**" %}}
Every `leoflow` command and flag, generated from Cobra.

[CLI commands →](/reference/cli/)
{{% /card %}}

{{% card header="**Go packages (GoDocs)**" %}}
GoDocs for the control plane, scheduler, executor, agent, and storage — one page
per package, each symbol linking to its source.

[Go packages →](/reference/go/)
{{% /card %}}
{{% /cardpane %}}

{{% cardpane %}}
{{% card header="**Python runtime API**" %}}
The task-runtime helpers your DAG code imports (XCom, staging paths).

[Python runtime →](/reference/python-api/)
{{% /card %}}

{{% card header="**Configuration**" %}}
The `LEOFLOW_*` environment variables and config keys for the server.

[Configuration →](/reference/configuration/)
{{% /card %}}

{{% card header="**MCP server**" %}}
The Model Context Protocol server — resources and tools that expose Leoflow to
agents.

[MCP server →](/reference/mcp/)
{{% /card %}}
{{% /cardpane %}}

The [Glossary](/reference/glossary/) collects the core vocabulary (DAG, DagRun,
XCom, Executor, …).
