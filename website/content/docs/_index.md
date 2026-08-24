---
title: Documentation
linkTitle: Docs
weight: 20
description: >
  GitOps-first, container-native workflow orchestrator with an Airflow-compatible
  UI and REST API — a Go control plane instead of Python's.
---

{{% pageinfo %}}
This is a **Phase 0 scaffold** of the Hugo + Docsy documentation site, migrated in
parallel with the live MkDocs-Material site. Only a handful of representative pages
are converted here to prove the toolchain end to end.
{{% /pageinfo %}}

Leoflow is a **Go control plane** that keeps Apache Airflow's proven **pod-per-task**
model and its **UI**, and throws away the Python control plane that makes Airflow
slow.

## Start here

- **[Why Leoflow](/docs/why-leoflow/)** — the five wounds Airflow won't heal, and
  how Leoflow heals them.
- **[Architecture](/docs/architecture/)** — the Go control plane, the split API /
  scheduler roles, and the execution data flow.
- **[On-failure alerting](/docs/alerting/)** — notify on failure from
  `leoflow.yaml`, no extra task and no Python.

## How a DAG works

A DAG is a `leoflow.yaml` (config, bindings, packaging) plus a `dag.py` (Airflow
SDK). They compile to one immutable artifact — `dag.json` plus a container image.
