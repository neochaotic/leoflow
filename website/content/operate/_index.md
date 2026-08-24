---
title: Deploy & operate
linkTitle: Deploy & operate
weight: 40
description: Take Leoflow to production — deploy from CI, run the Helm chart, upgrade, back up, and keep the scheduler resilient.
cascade: { type: docs }
menu:
  main:
    weight: 40
---

Running Leoflow in production: promotion from Lite to Pro, the deploy pipeline, and
the day-2 operations that keep a control plane healthy.

- **[Deploy your first Pro DAG](/operate/first-pro-dag/)** — take a DAG from Lite to
  a Kubernetes control plane.
- **[CI/CD & deploy](/operate/cicd-deploy/)** — build, push, and register DAGs from
  CI (GitHub Actions, GitLab, Cloud Build).
- **[Helm chart](/operate/helm-chart/)** — install and configure the Pro control
  plane on Kubernetes.
- **[Upgrades](/operate/upgrades/)** · **[Backup & restore](/operate/backup-restore/)**
  — day-2 lifecycle.
- **[Troubleshooting & observability](/operate/troubleshooting/)** — diagnose DAG,
  scheduler, and executor problems.
- **[Scheduler resilience](/operate/scheduler-resilience/)** ·
  **[Warm worker pools](/operate/warm-pools/)** — availability and latency.
- **[Agent credential transport](/operate/agent-credential-transport/)** ·
  **[Pro TLS](/operate/pro-tls/)** ·
  **[Staging volume](/operate/staging-volume/)** — the security and data-path
  internals.
