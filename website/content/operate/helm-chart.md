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

## Install from the published OCI chart

Every release tag publishes the chart as a **cosign-signed OCI artifact** to
`oci://ghcr.io/neochaotic/charts/leoflow` ([ADR 0028](/project/adrs/0028-release-versioning-two-editions/)),
co-versioned with the release tag — so you can install a pinned version without
cloning the repo:

```bash
helm install leoflow oci://ghcr.io/neochaotic/charts/leoflow --version <x.y.z> \
  -n leoflow --create-namespace \
  -f values.yaml
```

Pass the release tag **without** the leading `v` (tag `v0.4.0` → `--version 0.4.0`):
the chart `version`/`appVersion` move in lockstep with the tag, so this also pins
the control-plane image. Installing from a source checkout
(`helm install ./helm/leoflow`) is still supported for unreleased branches — see
the [chart README](https://github.com/neochaotic/leoflow/blob/main/helm/leoflow/README.md#quick-start)
for both paths and the full values surface.

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
- A guarded HA posture: `replicaCount > 1` refuses to render onto a single-writer
  log volume, and the PodDisruptionBudget turns itself on exactly when a second
  replica makes it safe — see
  [Control-plane HA and disruption posture](/operate/control-plane-ha/) and the
  one-switch `examples/values-ha.yaml` profile.
- Opt-in hardening templates: HPA, NetworkPolicy, and a Prometheus
  ServiceMonitor.
- TLS termination via cert-manager — see [Pro TLS](/operate/pro-tls/).

## Related

- [Editions & operating modes](/concepts/editions/) — what Pro gives you and its
  validation status.
- [Deploy your first Pro DAG](/operate/first-pro-dag/) — the promotion walkthrough.
- [Upgrades](/operate/upgrades/) — upgrading a chart release safely.
- Cross-listed from the [Reference](/reference/) section for the values surface.
