# Leoflow Helm Chart

Deploys the Leoflow **control plane** (`leoflow-server`) into Kubernetes for a
production-like install — distinct from the host-run `test/e2e/e2e.sh` smoke.

## What it installs

| Resource | Purpose |
|---|---|
| Deployment | `leoflow-server` (HTTP `8080`, metrics `9090`, agent gRPC `9091`) |
| Service | ClusterIP exposing http / metrics / grpc |
| ServiceAccount + Role/RoleBinding | lets the control plane create/watch/delete **task pods** and read their logs in `taskNamespace` |
| Secret | holds inline DB/Redis/JWT/bootstrap credentials (skipped when you bring your own) |
| Job (hook) | runs `golang-migrate` before install/upgrade |
| Ingress | optional |

## Quick start

```bash
kubectl create namespace leoflow
helm install lf ./helm/leoflow -n leoflow \
  --set database.url='postgres://user:pass@postgres:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://redis:6379/0' \
  --set auth.jwtSecret='change-me' \
  --set bootstrap.password='admin'
```

Task pods are created in `taskNamespace` (default `leoflow`, which must match the
namespace the server targets). Agents dial the control plane gRPC at the
in-cluster Service DNS automatically (override with
`config.agentControlPlaneAddr`).

## Bringing your own secrets

Instead of inline values, reference existing Secrets:

```bash
--set database.existingSecret=my-db     # key: databaseUrl
--set redis.existingSecret=my-redis     # key: redisUrl
--set auth.existingSecret=my-jwt        # key: jwtSecret
--set bootstrap.existingSecret=my-boot  # key: bootstrapPassword
```

When all credentials come from existing Secrets, the chart creates no Secret of
its own.

## Migrations

The pre-install/pre-upgrade Job runs `migrate -path <path> -database <url> up`
using the default image **`ghcr.io/neochaotic/leoflow-migrate`** (built from
`deploy/Dockerfile.migrate` via `make migrate-image`), which bundles the Leoflow
`migrations/` at `migrations.path`. Override `migrations.image` to use your own,
or set `migrations.enabled=false` to migrate out of band.

## Common values

| Key | Default | Notes |
|---|---|---|
| `image.repository` / `image.tag` | `ghcr.io/neochaotic/leoflow-server` / chart appVersion | server image |
| `replicaCount` | `1` | scheduler leader-elects, so >1 is safe |
| `taskNamespace` | `leoflow` | where task pods run; RBAC is granted here |
| `config.logsDir` | `/var/log/leoflow` | log sink dir (writable emptyDir is mounted) |
| `database.url` / `.existingSecret` | — | Postgres |
| `redis.url` / `.existingSecret` | — | Redis (XCom + locks) |
| `auth.jwtSecret` / `.existingSecret` | — | signs API + agent tokens |
| `migrations.enabled` | `true` | golang-migrate hook Job |
| `ingress.enabled` | `false` | HTTP ingress |

## Production hardening (draft)

Off by default; opt in per environment. See the full
[Production deployment guide](../../docs/production-deployment.md) for publishing,
CI (kind pod-per-task gate), and the GitOps delivery model.

| Key | Resource | Notes |
|---|---|---|
| `autoscaling.enabled` | `HorizontalPodAutoscaler` | safe with the leader-elected scheduler |
| `podDisruptionBudget.enabled` | `PodDisruptionBudget` | keep the API available across node drains |
| `networkPolicy.enabled` | `NetworkPolicy` | clients → HTTP, Prometheus → metrics, task pods → gRPC |
| `metrics.serviceMonitor.enabled` | `ServiceMonitor` | Prometheus Operator scrape of `/metrics` |
| `agentTLS.enabled` | — | mTLS for the agent ↔ control-plane gRPC (cert-manager, #58) |

## Validate

```bash
helm lint ./helm/leoflow
helm template lf ./helm/leoflow -n leoflow --set database.url=postgres://x --set auth.jwtSecret=s
```
