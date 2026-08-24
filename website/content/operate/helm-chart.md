---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /helm-chart.html
# --- end AUTO redirect aliases ---
title: Helm chart
linkTitle: Helm chart
weight: 30
description: Install and configure the Leoflow Pro control plane on Kubernetes with the official Helm chart.
---

The **Pro** control plane installs on Kubernetes via the official Helm chart. The
chart is chart-test gated, publishes multi-arch images per release, and signs them
with cosign. It runs on any cluster with an external Postgres + Redis.

{{% alert title="Reference lives with the chart" color="info" %}}
This operator-journey page is the entry point; the exhaustive values reference is
maintained **alongside the chart source** so it never drifts from `values.yaml`:

**[→ Helm chart README](https://github.com/neochaotic/leoflow/blob/main/helm/leoflow/README.md)**
(including the [datastore compatibility matrix](https://github.com/neochaotic/leoflow/blob/main/helm/leoflow/README.md#datastore-compatibility)).

A first-class values reference on this site is a TODO for a later migration phase.
{{% /alert %}}

## What the chart gives you

- The `/api/v2/` Airflow-compatible API + UI, and the scheduler — as one process
  (`role=all`) or split into `role=api` + `role=scheduler`
  ([ADR 0049](/project/adrs/0049-split-api-and-scheduler-roles/)).
- Opt-in hardening templates: HPA, PodDisruptionBudget, NetworkPolicy, and a
  Prometheus ServiceMonitor.
- TLS termination via cert-manager — see [Pro TLS](/operate/pro-tls/).

## Related

- [Editions & operating modes](/concepts/editions/) — what Pro gives you and its
  validation status.
- [Deploy your first Pro DAG](/operate/first-pro-dag/) — the promotion walkthrough.
- [Upgrades](/operate/upgrades/) — upgrading a chart release safely.
- Cross-listed from the [Reference](/reference/) section for the values surface.
