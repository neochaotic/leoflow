# PoC: Leoflow + Bitnami Postgres + Bitnami Redis on one cluster

> [!WARNING]
> **NOT FOR PRODUCTION.** This recipe is for evaluating Leoflow end-to-end on a
> single Kubernetes cluster (kind, minikube, k3d, a scratch GKE/EKS namespace).
> The Bitnami datastores below are configured for evaluation only: no
> persistence guarantees, no HA, no auth on Redis. For real deploys, point
> `database.url` / `redis.url` at managed services (RDS, ElastiCache, CloudSQL,
> Memorystore, your own HA cluster) and skip the Bitnami installs.

The chart's `templates/deployment.yaml` deliberately fails the install when
neither `database.url`/`existingSecret` nor `redis.url`/`existingSecret` is set
— Pro Leoflow refuses to silently fall back to embedded datastores (those are
a Lite-edition concept). This recipe satisfies that contract by installing
external-looking Postgres + Redis from Bitnami first, then pointing the chart
at them.

## Prerequisites

- A Kubernetes cluster (`kubectl cluster-info` works).
- `helm` v3.16+.
- Outbound access to `registry-1.docker.io` for the Bitnami charts.

## Steps

### 1. Namespace

```bash
kubectl create namespace leoflow-poc
```

### 2. Postgres (Bitnami)

```bash
helm install pg oci://registry-1.docker.io/bitnamicharts/postgresql \
  --version 16 \
  -n leoflow-poc \
  --set auth.username=leoflow \
  --set auth.password=leoflow-poc \
  --set auth.database=leoflow \
  --set primary.persistence.enabled=false
```

- `--version 16` pins the chart major. Bitnami occasionally rewrites
  service names + value keys across majors (e.g. redis chart v18 changed
  defaults). The PoC values file (`poc-with-bitnami.yaml`) hard-codes
  `pg-postgresql` as the service name; that holds for major 16.x.
- Service name: `pg-postgresql` (release name `pg` + chart suffix).
- `persistence.enabled=false` skips the PVC so the PoC tears down cleanly. Drop
  this flag if you want the data to survive pod restarts.

Wait for it: `kubectl -n leoflow-poc rollout status statefulset/pg-postgresql --timeout=180s`.

### 3. Redis (Bitnami)

```bash
helm install redis oci://registry-1.docker.io/bitnamicharts/redis \
  --version 20 \
  -n leoflow-poc \
  --set auth.enabled=false \
  --set architecture=standalone \
  --set master.persistence.enabled=false
```

- `--version 20` pins the chart major. The PoC values file hard-codes
  `redis-master` as the service name; that holds for major 20.x.
- `auth.enabled=false` matches the URI in `poc-with-bitnami.yaml`
  (`redis://redis-master:6379/0`). Set a password and update the URI in the
  values file for any shared cluster.
- `architecture=standalone` skips the replicas: one Redis pod, simpler PoC.

Wait for it: `kubectl -n leoflow-poc rollout status deploy/redis-master --timeout=120s` (or `statefulset/redis-master` depending on chart version).

### 4. Leoflow

```bash
helm install leoflow ./helm/leoflow \
  -n leoflow-poc \
  -f helm/leoflow/examples/poc-with-bitnami.yaml
```

The `poc-with-bitnami.yaml` file in this directory carries the matching
`database.url` / `redis.url` plus fixture credentials.

Wait for it: `kubectl -n leoflow-poc rollout status deploy/leoflow --timeout=240s`.

## Verify

```bash
kubectl -n leoflow-poc port-forward svc/leoflow 8080:8080 &
curl -fsS http://127.0.0.1:8080/readyz
```

Open `http://127.0.0.1:8080/` for the UI. Log in with `admin` /
`leoflow-poc-admin` (the `bootstrap.password` from the values file).

## Teardown

```bash
helm uninstall -n leoflow-poc leoflow
helm uninstall -n leoflow-poc redis
helm uninstall -n leoflow-poc pg
kubectl delete namespace leoflow-poc
```

`helm uninstall` on the Bitnami charts leaves PVCs behind by default unless
you ran with `persistence.enabled=false` (recipe above did). Delete leftover
PVCs with `kubectl -n leoflow-poc delete pvc --all` if anything sticks.

## When you're ready for production

Drop the Bitnami installs and the PoC values file. Point at managed
datastores via either inline values or existing Secrets:

```bash
helm install leoflow ./helm/leoflow -n leoflow \
  --set database.url='postgres://...@<managed-pg>:5432/leoflow?sslmode=require' \
  --set redis.url='rediss://...@<managed-redis>:6380/0' \
  --set auth.jwtSecret="$(openssl rand -base64 64)" \
  --set secretKey="$(openssl rand -hex 16)"  # 32 raw bytes via hex
```

See the main chart README (`helm/leoflow/README.md`) for the full values
reference, and `docs/upgrades.md` / `docs/backup-restore.md` for ongoing
operations.
