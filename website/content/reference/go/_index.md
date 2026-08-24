---
title: Go packages (GoDocs)
linkTitle: Go packages
weight: 30
description: GoDocs for the control plane, scheduler, executor, agent, and storage — one page per package.
cascade: { type: docs }
---

Leoflow's control plane, agent, and CLI are Go. Every exported identifier carries a
GoDoc (Go Report Card A+ is the quality floor), and each symbol links to its source
on GitHub. One page per package keeps each reference a readable length.

{{% alert title="Generated content — wiring pending" color="info" %}}
The per-package pages below are **generated from source** by the GoDoc export step
and written into this directory at build time. That generator is being ported from
the MkDocs pipeline into the Hugo build; until it runs in CI the individual package
pages are not yet present here.
{{% /alert %}}

Packages exported to this reference:

- `pkg/client` — the typed `/api/v2` client (**public**)
- `internal/domain` — core types (DAG, Task, Run, …)
- `internal/scheduler` — the state machine
- `internal/executor` — K8s / subprocess executors
- `internal/dispatch` — executor routing
- `internal/agent` — the in-container agent
- `internal/agentrpc` — agent gRPC
- `internal/storage` — Postgres / Redis
- `internal/auth` — JWT + RBAC
- `internal/config` — configuration
- `internal/cli` — the `leoflow` CLI
- `internal/api` — HTTP handlers
