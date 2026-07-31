# leoflow

![Version: 0.1.2-rc.2](https://img.shields.io/badge/Version-0.1.2--rc.2-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.1.2-rc.2](https://img.shields.io/badge/AppVersion-0.1.2--rc.2-informational?style=flat-square)

Leoflow control plane — a GitOps-first, container-native workflow orchestrator (Airflow 3.2.x UI/API compatible).

Deploys the Leoflow **control plane** (`leoflow-server`) into Kubernetes for a
production-like install — distinct from the host-run `test/e2e/e2e.sh` smoke.

**Homepage:** <https://github.com/neochaotic/leoflow>

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

> **Upgrades + secret rotation.** The pod template carries a `checksum/secret`
> annotation hashed over the **chart-rendered** Secret (#316), so `helm upgrade`
> rolls the pod whenever an **inline** value backing it changes
> (`database.url`, `redis.url`, `auth.jwtSecret`, `secretKey`,
> `bootstrap.password`). Credentials wired via `*.existingSecret` are **outside
> the chart's visibility**: rotating them requires a manual
> `kubectl rollout restart deployment/leoflow` for the change to take effect.

## Verified TLS to managed Postgres (#315)

Managed Postgres (Cloud SQL, RDS, Azure DB) presents a server cert signed by
a provider / per-instance CA that is **not** in the container's system roots.
Without the CA bundle mounted, the strongest TLS posture you can pin is
`sslmode=require` — encrypted but the server cert is **not verified**
(MITM-vulnerable). To upgrade to `sslmode=verify-full`:

1. Create a ConfigMap holding the CA bundle as `ca.crt`:

   ```bash
   kubectl create configmap managed-pg-ca \
     -n leoflow --from-file=ca.crt=server-ca.pem
   ```

2. Reference it from `database.caConfigMap` and point the DSN at the mounted
   path:

   ```bash
   helm upgrade --install leoflow ./helm/leoflow -n leoflow \
     --set database.caConfigMap=managed-pg-ca \
     --set database.url='postgres://user:pass@cloudsql-private-ip:5432/leoflow?sslmode=verify-full&sslrootcert=/etc/leoflow/db-ca/ca.crt'
   ```

The chart mounts the ConfigMap readonly at `/etc/leoflow/db-ca/ca.crt`. pgx
reads `sslrootcert` from the DSN natively, so no extra Go-side configuration
is required.

> **Rotation note.** Kubernetes auto-updates the mounted file when the
> ConfigMap changes, but the pgx pool keeps its existing connections until
> they cycle. Cert rotation that invalidates the old chain may break in-flight
> connections; rolling the pod (`kubectl rollout restart deploy/leoflow`)
> guarantees a clean cutover.

> **Redis sibling — coming next** (#312). The same pattern lands for Redis in
> a follow-up PR: a `redis.caConfigMap` knob mounts the bundle and the Go
> client overrides its `TLSConfig.RootCAs` from the file (go-redis does not
> read CA paths from the URL the way pgx does).

## Migrations

The pre-install/pre-upgrade Job runs `migrate -path <path> -database <url> up`
using the default image **`ghcr.io/neochaotic/leoflow-migrate`** (built from
`deploy/Dockerfile.migrate`, published by `.github/workflows/release.yaml` on
every tag), which bundles the Leoflow `migrations/` at `migrations.path`.
Override `migrations.image` to use your own, or set `migrations.enabled=false`
to migrate out of band.

## Datastore compatibility

The chart talks to external Postgres + Redis you provision. Versions
we test against in CI and the minimums we support:

| Datastore | Tested in CI | Recommended minimum | Notes |
|---|---|---|---|
| **PostgreSQL** | 16.x (`postgres:16-alpine`) | **PostgreSQL 13** (current LTS line) | Uses JSONB, `ON CONFLICT`, advisory locks, generated columns. Versions below 13 may work but are untested; CI doesn't validate them. |
| **Redis** | 7.x (`redis:7-alpine`) | **Redis 6.0** | ACL is used for the per-tenant auth model; Redis 5 and below lack ACL. Streams aren't used today but may be in the future. |

The PoC recipe pins Bitnami chart majors that bundle the tested
versions (postgres chart 16 → PG 16; redis chart 20 → Redis 7). When
pointing at managed cloud datastores (RDS, ElastiCache, CloudSQL,
Memorystore), check that the engine version is at the recommended
minimum or above.

If you need to run against an older engine, please file an issue with
the version + symptoms — we don't bench-test older majors but will
investigate specific incompatibilities.

## Verified TLS to managed Redis (#312)

Managed Redis offerings — Memorystore SERVER_AUTHENTICATION, ElastiCache
in-transit encryption, Azure Cache for Redis — sign their TLS server cert
with a provider or per-instance CA that is **not** in the container's
system roots. Without telling the client which CA to trust, the only
working postures are "plaintext `redis://`" (unacceptable across the
internet) or "skip verification" (we don't expose that knob; it strips
TLS to a noise channel).

Set `redis.caConfigMap` to a ConfigMap holding the provider CA as
`ca.crt`:

```bash
kubectl create configmap leoflow-redis-ca \
  --from-file=ca.crt=./memorystore-server-ca.pem -n leoflow
```

```yaml
redis:
  url: rediss://10.0.0.5:6378/0
  caConfigMap: leoflow-redis-ca
```

The chart then:

1. Mounts the ConfigMap read-only at `/etc/leoflow/redis-ca/ca.crt`.
2. Sets `LEOFLOW_REDIS_CA_FILE=/etc/leoflow/redis-ca/ca.crt` so the
   server overrides `tls.Config.RootCAs` instead of falling back to
   system roots.
3. Refuses to boot if the file is missing or malformed (clear error
   instead of a confusing "x509: certificate signed by unknown
   authority" at first Ping).

Leave `caConfigMap` empty for plaintext `redis://` or for managed
TLS that uses a public CA (rare).

## Evaluating without a managed Postgres + Redis

For a one-cluster evaluation (kind, minikube, k3d, scratch namespace), the
chart deliberately won't fall back to embedded datastores — that's Lite's
job, not Pro's (see `templates/deployment.yaml:8-13`). The supported PoC
path is to install Bitnami's Postgres + Redis charts alongside Leoflow:

- Recipe: [`helm/leoflow/examples/README.md`](https://github.com/neochaotic/leoflow/tree/main/helm/leoflow/examples/README.md)
- Matching values file: [`helm/leoflow/examples/poc.yaml`](https://github.com/neochaotic/leoflow/tree/main/helm/leoflow/examples/poc.yaml)

Three `helm install`s in total. **Not for production** — see the recipe for
the production-shaped command.

## Validate

```bash
helm lint ./helm/leoflow
helm template lf ./helm/leoflow -n leoflow \
  --set database.url=postgres://x \
  --set redis.url=redis://r/0 \
  --set auth.jwtSecret=s \
  --set "secretKey=$(openssl rand -hex 32)"
bash scripts/helm-template-checks.sh   # contract assertions (env wiring, Job hardening, fixture lengths)
```

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| Leoflow |  |  |

## Source Code

* <https://github.com/neochaotic/leoflow>

## Values

The table below is auto-generated by [`helm-docs`](https://github.com/norwoodj/helm-docs) from `values.yaml`. To
update: edit `values.yaml` (use `# -- <description>` comments above each key
that warrants documentation), then run `helm-docs -c helm/leoflow` from the
repo root. CI fails (helm-ci.yaml lint job) if the regenerated README would
differ from what's committed.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod affinity rules (standard K8s — co-locate or anti-affinity). |
| agentTLS.caConfigMap | string | `""` | Name of a ConfigMap with key `ca.crt`. Mounted into task pods so the agent verifies the server cert. Typically a cert-manager trust-bundle. |
| agentTLS.enabled | bool | `true` | Enable TLS on the agent ↔ control plane gRPC channel (#58). Keep this `true`: the chart marks every deployment as the production edition (`LEOFLOW_UI_EDITION=pro`), and that edition refuses to boot without a gRPC cert/key — setting this to `false` yields a crash-looping control plane ("the Pro edition requires TLS on the agent gRPC channel"), not a plaintext deployment. The same edition marker also rejects `LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true`, so secrets cannot accidentally travel a plaintext channel. For a plaintext local loop use the Lite dev server, not this chart. |
| agentTLS.serverCertSecret | string | `""` | Name of a `kubernetes.io/tls` Secret (with `tls.crt`/`tls.key`) for the gRPC server. Typically produced by a cert-manager `Certificate`. Required when `enabled: true`. |
| auth.existingSecret | string | `""` | Name of a Secret with key `jwtSecret` (takes precedence over `jwtSecret`). |
| auth.jwtSecret | string | `""` | HMAC secret signing API + agent JWTs. Set inline OR reference an existing Secret via `existingSecret`. Generate with `openssl rand -base64 64`. |
| auth.tokenTtlSeconds | int | `3600` | API + agent JWT lifetime in seconds. Default 1h; raise for longer agent sessions. |
| autoscaling.behavior | object | `{}` | HPA scaling behavior (scale-up/scale-down policies). See K8s docs for autoscaling/v2 `behavior` schema. |
| autoscaling.enabled | bool | `false` | Enable HPA for the leoflow-server Deployment. Requires metrics-server. |
| autoscaling.maxReplicas | int | `6` | Maximum replicas. HPA never scales above this. |
| autoscaling.minReplicas | int | `2` | Minimum replicas. HPA never scales below this. |
| autoscaling.targetCPUUtilizationPercentage | int | `70` | Target average CPU utilization across replicas (percent). HPA scales out when exceeded. |
| autoscaling.targetMemoryUtilizationPercentage | string | `""` | Target average memory utilization (percent). Empty = not used. Add only if your workload is memory-bound (rare for a control plane). |
| bootstrap.existingSecret | string | `""` | Name of a Secret with key `bootstrapPassword` (takes precedence over `password`). |
| bootstrap.password | string | `""` | Initial admin password (first install only). Leave empty to skip the bootstrap; the operator then creates the first admin out-of-band. |
| config.agentControlPlaneAddr | string | `""` | gRPC address task pods dial back to reach the control plane. Defaults to the in-cluster Service DNS on `ports.grpc` when empty. Override for cross-cluster or external task pods. |
| config.cors.allowedOrigins | list | `["*"]` | CORS allowed origins for the API. `["*"]` is fine for Pro behind an authenticated ingress; tighten for public-facing deploys. |
| config.logsDir | string | `"/var/log/leoflow"` | Directory inside the pod where task logs are written. Mounted from `logs.persistence` (a PVC by default) so logs survive pod restarts. Set `logs.persistence.enabled: false` to fall back to an ephemeral emptyDir (dev only). |
| config.scheduler.enabled | bool | `true` | Run the scheduler loop. Disable only for read-only API-only replicas (rare). |
| config.scheduler.loopIntervalMs | int | `1000` | Scheduler loop interval in milliseconds. Lower = faster reactivity, higher CPU. 1000ms is the production-tested default. |
| database.caConfigMap | string | `""` | Name of a ConfigMap with key `ca.crt` holding the managed-Postgres CA bundle (#315). When set, the chart mounts it at `/etc/leoflow/db-ca/ca.crt` so the operator can pin `sslmode=verify-full&sslrootcert=/etc/leoflow/db-ca/ca.crt` in the DSN. Empty (default) means TLS still works via `sslmode=require`, but the server cert is NOT verified — the connection is encrypted but MITM-vulnerable, the standard managed-DB posture before this knob. |
| database.existingSecret | string | `""` | Name of a Secret with key `databaseUrl` (takes precedence over `url`). |
| database.maxIdleConns | int | `5` | Max idle DB connections kept in the pool. Should be ≤ `maxOpenConns`. |
| database.maxOpenConns | int | `20` | Max concurrent open DB connections (Postgres-side load gate). Increase for high-throughput Pro deployments. |
| database.url | string | `""` | External Postgres DSN. Required for Pro (the embedded datastore is Lite-only); the chart `fail`s the install if neither this nor `existingSecret` is set. Example: `postgres://user:pass@host:5432/leoflow?sslmode=disable`. |
| image.pullPolicy | string | `"IfNotPresent"` |  |
| image.repository | string | `"ghcr.io/neochaotic/leoflow-server"` | Control-plane image. Published by GoReleaser on every tag, signed with cosign. |
| image.tag | string | `""` | Image tag. Defaults to `.Chart.appVersion` when empty; pre-alpha installs should pin `--set image.tag=v0.0.1-prealpha.N` (the `v`-prefix and no-`v` forms are both published and resolve to the same digest, so either works). |
| imagePullSecrets | list | `[]` |  |
| ingress.annotations | object | `{}` | Ingress annotations (controller-specific: rewrites, TLS, auth, etc.). |
| ingress.className | string | `""` | Ingress class name (e.g. `nginx`). Leave empty to use the cluster default. |
| ingress.enabled | bool | `false` | Enable an Ingress resource exposing the control plane via HTTP/HTTPS. Requires an Ingress controller (nginx/traefik/etc.) in the cluster. |
| ingress.hosts | list | `[{"host":"leoflow.local","paths":[{"path":"/","pathType":"Prefix"}]}]` | Host + path rules. Each host maps to one or more path entries routed to the leoflow-server's `http` port. |
| ingress.tls | list | `[]` | TLS configuration. Each entry maps hosts to a TLS Secret (typically a cert-manager Certificate Secret). |
| logs.persistence.accessMode | string | `"ReadWriteOnce"` | PVC access mode. `ReadWriteOnce` (default) is fine for single-replica deployments; `ReadWriteMany` is required when `replicaCount > 1`. |
| logs.persistence.enabled | bool | `true` | Persist control-plane logs in a PVC (default ON). Disable for ephemeral emptyDir (dev only — logs lost on pod restart). |
| logs.persistence.size | string | `"50Gi"` | PVC size for control-plane logs. ~1 GB/day per ~1000 active task runs is a sane starting point. |
| logs.persistence.storageClass | string | `""` | StorageClass for the PVC. Empty uses the cluster default. Specify an RWX class when `accessMode: ReadWriteMany`. |
| metrics.serviceMonitor.additionalLabels | object | `{}` | Extra labels on the ServiceMonitor. Required when the Prometheus instance has a `serviceMonitorSelector` filter (e.g. `{release: kube-prometheus-stack}`). |
| metrics.serviceMonitor.enabled | bool | `false` | Enable ServiceMonitor for Prometheus scraping. Requires kube-prometheus-stack CRDs. |
| metrics.serviceMonitor.interval | string | `"30s"` | Prometheus scrape interval. |
| metrics.serviceMonitor.namespace | string | `""` | Namespace for the ServiceMonitor resource. Defaults to the release namespace; override when Prometheus expects ServiceMonitors in a dedicated namespace. |
| metrics.serviceMonitor.scrapeTimeout | string | `"10s"` | Prometheus scrape timeout (must be ≤ interval). |
| migrations.enabled | bool | `true` |  |
| migrations.image.pullPolicy | string | `"IfNotPresent"` |  |
| migrations.image.repository | string | `"ghcr.io/neochaotic/leoflow-migrate"` | leoflow-migrate image bundling Leoflow SQL migrations on top of `migrate/migrate`. Published per release by `release.yaml`, signed with cosign, multi-arch (amd64 + arm64). |
| migrations.image.tag | string | `""` | Migration image tag. Defaults to `.Chart.appVersion` when empty. Pin to the same tag as `image.tag` (both server and migrate publish both `v`-prefix and no-`v` forms — use whichever convention you prefer, they resolve to the same digest): `--set migrations.image.tag=v0.0.1-prealpha.N`. |
| migrations.path | string | `"/migrations"` | Path inside the migrate image where the SQL files live. Must match the COPY destination in `deploy/Dockerfile.migrate`. |
| migrations.podSecurityContext.fsGroup | int | `65532` |  |
| migrations.podSecurityContext.runAsGroup | int | `65532` |  |
| migrations.podSecurityContext.runAsNonRoot | bool | `true` |  |
| migrations.podSecurityContext.runAsUser | int | `65532` |  |
| migrations.securityContext.allowPrivilegeEscalation | bool | `false` |  |
| migrations.securityContext.capabilities.drop[0] | string | `"ALL"` |  |
| migrations.securityContext.readOnlyRootFilesystem | bool | `true` |  |
| migrations.securityContext.runAsNonRoot | bool | `true` |  |
| migrations.securityContext.runAsUser | int | `65532` |  |
| networkPolicy.egress | list | `[]` | Explicit egress rules. Empty = allow-all (DNS is ALWAYS allowed regardless). Lock down to your DB/Redis/kube-apiserver endpoints in regulated environments. |
| networkPolicy.enabled | bool | `false` | Enable NetworkPolicy gating ingress + egress on the control-plane pods. Requires a CNI that enforces policies (Calico/Cilium/etc.). |
| networkPolicy.ingressFrom | list | `[]` | NetworkPolicy `from` rules for HTTP + gRPC ingress (task pods dial back). Empty = allow from any pod in any namespace. Tighten with e.g. `[{namespaceSelector: {}}]` for same-namespace only. |
| networkPolicy.metricsFrom | list | `[]` | NetworkPolicy `from` rules for the metrics port (Prometheus scrape). Empty = no separate rule; the metrics port is reachable from wherever `ingressFrom` allows. Set e.g. `[{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: monitoring}}}]` to restrict to a Prometheus namespace. |
| nodeSelector | object | `{}` | Pod nodeSelector (standard K8s scheduling label match). |
| observability.logFormat | string | `"json"` | Log format: `json` (production / log aggregators) or `console` (dev / human-readable). |
| observability.logLevel | string | `"info"` | Log level: `debug`, `info`, `warn`, `error`. Production default is `info`. |
| observability.otel.enabled | bool | `false` | Export OpenTelemetry traces. When false, internal spans are no-ops. |
| observability.otel.endpoint | string | `""` | OTLP/gRPC endpoint URL, e.g. `otel-collector:4317`. Required when `otel.enabled: true`. |
| podAnnotations | object | `{}` |  |
| podDisruptionBudget.enabled | bool | `false` | Enable PDB for the leoflow-server Deployment. Pair with replicaCount > 1. |
| podDisruptionBudget.maxUnavailable | string | `""` | Maximum replicas allowed unavailable during voluntary disruption. Set only ONE of minAvailable / maxUnavailable. |
| podDisruptionBudget.minAvailable | int | `1` | Minimum replicas that must remain up during voluntary disruption. Set only ONE of minAvailable / maxUnavailable. |
| podSecurityContext.fsGroup | int | `65532` |  |
| podSecurityContext.runAsGroup | int | `65532` |  |
| podSecurityContext.runAsNonRoot | bool | `true` |  |
| podSecurityContext.runAsUser | int | `65532` |  |
| ports | object | `{"grpc":9091,"http":8080,"metrics":9090}` | Ports the leoflow-server listens on. http: API + UI, metrics: Prometheus /metrics, grpc: agent ↔ control plane channel (task pods dial back here). |
| rbac.create | bool | `true` | Create the Role + RoleBinding granting the control plane create/get/list/watch/delete on pods + get on pods/log in `taskNamespace`. Required for the pod-per-task executor. |
| redis.caConfigMap | string | `""` | Name of a ConfigMap with a `ca.crt` key containing the PEM CA bundle the client trusts when negotiating TLS to a `rediss://` URL (#312). Required when the managed-Redis server cert is signed by a provider / per-instance CA that is not in the system roots — Memorystore SERVER_AUTHENTICATION, ElastiCache in-transit encryption, Azure Cache for Redis. Mounted read-only at `/etc/leoflow/redis-ca` and exposed to the server via `LEOFLOW_REDIS_CA_FILE`. Leave empty when Redis uses a public CA or no TLS. |
| redis.existingSecret | string | `""` | Name of a Secret with key `redisUrl` (takes precedence over `url`). |
| redis.url | string | `""` | External Redis URI. Required for Pro (the embedded XCom is Lite-only). Example: `redis://host:6379/0`, or `rediss://host:6380/0` for TLS. |
| replicaCount | int | `1` | Number of control-plane replicas. The scheduler leader-elects (ADR 0009), so `>1` is HA-safe (active-passive scheduler, active-active API). |
| resources | object | `{"limits":{"cpu":"1","memory":"512Mi"},"requests":{"cpu":"100m","memory":"128Mi"}}` | Resource requests + limits for the leoflow-server container. Defaults sized for a small Pro (50–500 DAGs); bump CPU+memory for larger deployments. The scheduler's main load is DB polling, not in-process compute. |
| secretKey | string | `""` | AES-256 key encrypting Connection passwords + Extra at rest (ADR 0019). MUST be exactly 32 raw bytes OR 64-char hex OR base64-of-32-bytes. Without it, Connection management is disabled (Variables still work). |
| secretKeyExistingSecret | string | `""` | Name of a Secret with key `secretKey` (takes precedence over `secretKey`). |
| securityContext.allowPrivilegeEscalation | bool | `false` |  |
| securityContext.capabilities.drop[0] | string | `"ALL"` |  |
| securityContext.readOnlyRootFilesystem | bool | `false` |  |
| securityContext.runAsNonRoot | bool | `true` |  |
| securityContext.runAsUser | int | `65532` |  |
| service.annotations | object | `{}` | Service annotations (e.g. cloud LB controller hints, ExternalDNS). |
| service.type | string | `"ClusterIP"` | Service type. `ClusterIP` for internal-only; `LoadBalancer` to expose externally; `NodePort` for k3d/kind. |
| serviceAccount.annotations | object | `{}` | ServiceAccount annotations (e.g. AWS IAM role: `eks.amazonaws.com/role-arn`). |
| serviceAccount.create | bool | `true` | Create a dedicated ServiceAccount for the leoflow-server. Set `false` only if you bring your own via `name`. |
| serviceAccount.name | string | `""` | Override the ServiceAccount name. Defaults to the chart fullname when empty. |
| taskNamespace | string | `"leoflow"` | Namespace where the control plane creates task pods. MUST match the namespace the server expects (server code currently targets `leoflow`). The chart grants the control plane RBAC to manage pods here; if you override this, the RBAC follows but the server still looks at `leoflow`. |
| taskSecret.mountPath | string | `"/etc/leoflow/secrets"` | Read-only mount path in the task pod. A connection references files here, e.g. `/etc/leoflow/secrets/key.json`. |
| taskSecret.name | string | `""` | Name of an existing Kubernetes Secret to mount into task pods. Empty = none. |
| taskServiceAccount.annotations | object | `{}` | Annotations. GKE Workload Identity: `iam.gke.io/gcp-service-account: GSA@PROJECT.iam.gserviceaccount.com`. EKS IRSA: `eks.amazonaws.com/role-arn: ...`. |
| taskServiceAccount.create | bool | `false` | Create a ServiceAccount in taskNamespace for task pods to run as. |
| taskServiceAccount.imagePullSecrets | list | `[]` | Pull secrets for private DAG images. Kubernetes auto-injects these into every task pod that runs as this SA, so a `leoflow deploy` to a private registry can be pulled (ADR 0041). Reference an existing docker-registry secret, e.g. `[{name: regcred}]`. |
| taskServiceAccount.name | string | `"leoflow-task"` | Name of the task ServiceAccount (use this as `execution.service_account`). |
| tolerations | list | `[]` | Pod tolerations (standard K8s — allow scheduling on tainted nodes). |
