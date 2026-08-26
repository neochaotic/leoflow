# RC cluster-validation runbook

**Reusable per RC and per cloud.** Hand this file to whoever (or whatever agent) has cluster access; fill in the run header and follow it top to bottom.
**Why this exists:** CI runs the pod-path E2E on **k3d + kindnet**, which cannot
enforce NetworkPolicy, provide ReadWriteMany, model cloud IAM (IRSA / Workload
Identity), real StorageClasses, node pools, eviction, or apiserver-scale watch. A
whole class of behavior is therefore only provable on a real cloud cluster. ADR
0033 leaves the human RC verification open ("whatever the rc is meant to
validate") — this file is that checklist.

**How to use:** fill in the run header, follow §0→§6, record PASS/FAIL in §6, and
open one issue per FAIL (root-caused, file:line if known — same bar as the
#722–#729 batch). For the **next** RC, keep §0/§2/§4/§5 as-is and only refresh §3
(the "features this RC shipped") from that release's CHANGELOG `[Unreleased]` /
release notes.

---

## Run header (fill in)

| Field | Value |
|---|---|
| RC under test | `v0.4.0-rc.3` |
| Cloud / cluster | AWS **EKS** (context: `<kubectl-context>`) |
| CNI | AWS VPC CNI (note if using Calico/Cilium overlay) |
| Node provisioning | (managed node group / Karpenter?) |
| Server image | `ghcr.io/neochaotic/leoflow-server:v0.4.0-rc.3` |
| Date / operator | `<date>` / `<who>` |

Links: release <https://github.com/neochaotic/leoflow/releases/tag/v0.4.0-rc.3> ·
Helm guide <https://neochaotic.github.io/leoflow/operate/helm-chart/> ·
chart README (full values) <https://github.com/neochaotic/leoflow/blob/main/helm/leoflow/README.md> ·
install page <https://neochaotic.github.io/leoflow/get-started/installation/>.

---

## §0 Prerequisites

- `kubectl` pointed at the target EKS cluster; `helm` v3.
- **External Postgres + Redis** reachable from the cluster (the chart runs on any
  cluster with external datastores — see the chart README "datastore
  compatibility" for the exact secret/URL value keys; do not guess them).
- A container registry the cluster can pull from for **your DAG images** (ECR).
  The control-plane image comes from `ghcr.io/neochaotic/leoflow-server:<rc>`.
- **Cloud identity** for keyless auth: an **IRSA** role (`eks.amazonaws.com/role-arn`)
  to annotate the ServiceAccounts (see §3/#725, §5).
- The `leoflow` CLI locally (client):
  `LEOFLOW_VERSION=v0.4.0-rc.3 curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/install.sh | sh`
  (explicit tag — "latest" skips pre-releases).

---

## §1 Install (Helm / Pro)

> **Before the tag is cut (maintainer preflight).** The chart `version` and
> `appVersion` in `helm/leoflow/Chart.yaml` must equal the release tag (minus the
> leading `v`) **before** you `git tag` — ADR 0028 keeps them in lockstep, and the
> image tags default to `.Chart.AppVersion`, so a stale Chart.yaml makes a default
> `helm install ./helm/leoflow` pull the **previous** release's images (this bit
> `v0.4.0-rc.3`, which shipped with Chart.yaml still on `rc.2` — arestas #3).
> Bump both keys, then verify with:
>
> ```bash
> scripts/check-chart-version-matches-tag.sh vX.Y.Z   # or $GITHUB_REF_NAME in CI
> ```
>
> The same gate runs automatically on every `v*` tag in
> `.github/workflows/helm-release.yaml`, so a cut with a stale chart fails the
> Helm chart release job rather than shipping wrong image defaults. Once tagged,
> install the **published OCI chart** (`--version` = tag without the `v`) rather
> than a source checkout — see the [chart README](https://github.com/neochaotic/leoflow/blob/main/helm/leoflow/README.md#quick-start).

Confirmed chart value keys used below (defaults in parens) — see the chart README
for the datastore/secret keys this runbook intentionally does not spell out:

- `split.enabled` (false) — `true` splits into `role=api` + `role=scheduler` (ADR 0049).
- `replicaCount` (1).
- `serviceAccount.annotations` / `taskServiceAccount.annotations` — IRSA role-arn.
- `rbac.create` (true) — renders the executor Role (+ the cluster-scoped
  `tokenreviews` grant when `auth.agentTokenTransport=exchange`).
- `auth.agentTokenTransport` (`envvar`) / `auth.secretLivenessMode` (`observe`).
- `execution.warmPoolsEnabled` (false) — requires `agentTokenTransport=exchange`
  **and** `secretLivenessMode=enforce` (chart refuses to render otherwise).
- `config.trustedProxies` (`[]`) — **#725**, must be reachable now.
- `executor.defaults.resources.cpu` / `.memory` (`""`) — **#725**, QoS defaults.
- `logs.persistence.enabled` / `.accessMode` (`ReadWriteOnce`) / `.storageClass`.

Baseline install (single-replica, dedicated pod-per-task, defaults):

```bash
helm install leoflow oci-or-repo/leoflow \
  --version <chart-version-for-rc> \
  -n leoflow --create-namespace \
  -f values.eks.yaml
```

Record: does the control plane reach `Ready`? Is `/api/v2/` + the UI reachable
(via port-forward or the ingress)? Can you `leoflow auth login` and get a JWT?

---

## §2 Smoke — the path a user actually runs

1. `leoflow auth login` → JWT.
2. Register + trigger a DAG (author per
   <https://neochaotic.github.io/leoflow/author-dags/dag-authoring/>; `leoflow push` / `leoflow deploy`).
3. **PASS:** every task instance reaches `success` — i.e. a real pod-per-task ran,
   its agent reported over gRPC, XCom chained. `kubectl get pods -n <taskNamespace>`
   shows one pod per task, completed.

---

## §3 rc.3 feature validation (refresh this section per RC)

The rc.3 tranche (#722–#729). ✔ = also unit/helm-verified; ★ = **only a real cluster proves it well**.

- **#722 secret-audit ✔** — with `secretLivenessMode=observe`, run a DAG that
  declares fewer secrets than the vault. **PASS:** `GET /api/v2/eventLogs` shows a
  `secret.scope_warning` row (not just a log line). Flip a scenario to `enforce`
  and confirm a `secret.liveness_denied` row is written too.
- **#723 reaper try-number ★** — see §4.2.
- **#724 validation 400 ✔** — register a DAG version declaring an unknown
  connection. **PASS:** API returns **400** (not 500); message points at
  `leoflow connections set`.
- **#725 config-bind + QoS ★** — see §4.3.
- **#726 gRPC key off api ★** — see §4.1 (split mode only).
- **#727 migration-job SA token ✔** — `kubectl get job <migrate> -o yaml`.
  **PASS:** `spec.template.spec.automountServiceAccountToken: false`.
- **#728 warm TMPDIR ★** — see §4.2 (warm pools).
- **#729 managed-PG idempotent** — Lite/local, not EKS. Run the opt-in E2E
  `test/e2e/lite-managed-pg-reextract.sh` on a networked host with managed-PG.

Also confirm the **rc.2 behavior change** still holds: **task pods run as non-root
by default** — `kubectl get pod <task> -o jsonpath='{.spec.securityContext}'`;
root-assuming DAG images now fail (expected).

---

## §4 Deep cluster-only checks (k3d cannot show these)

### §4.1 — #726: api role never receives the gRPC TLS private key (split mode)
```bash
helm upgrade leoflow ... --set split.enabled=true --set agentTLS.enabled=true --set edition=pro
kubectl get deploy -n leoflow            # expect an api and a scheduler deployment
```
- **PASS:** the **api** pod's projected volumes contain **no `tls.key`**
  (`kubectl get pod <api-pod> -o yaml | grep -A3 grpc-tls` → nothing on api,
  present on scheduler), the api pod **boots** (TLS env vars still set so the Pro
  boot guard passes), and secrets RPC scheduler→pod still flows (a task succeeds).

### §4.2 — #723 (retry wedge) + #728 (warm TMPDIR)
Enable warm pools (forces the security prereqs):
```bash
helm upgrade leoflow ... \
  --set execution.warmPoolsEnabled=true \
  --set auth.agentTokenTransport=exchange \
  --set auth.secretLivenessMode=enforce
```
- **#723 PASS:** force a lingering `Pending` try-1 pod (an unschedulable
  `nodeSelector`, or a bad image → `ImagePullBackOff`), let the task retry to try 2,
  and confirm the try-2 TI **fails/recovers** rather than sticking in `queued`
  forever. *(EKS makes this more reachable — Karpenter cold nodes + ECR pull
  latency produce long-lived Pending pods.)*
- **#728 PASS:** a warm worker serving two attempts of the same dag_version: write
  a sentinel file under `$TMPDIR` in attempt N, assert it is **absent** at the
  start of attempt N+1 on the same pod. Also check IRSA-derived creds cached to
  `~/.aws/cli/cache` — those live under `$HOME` and, per the shipped decision,
  **still persist** (mitigation: `read_only_task_root_filesystem` for warm pods).

### §4.3 — #725: QoS + ingress ClientIP
```bash
helm upgrade leoflow ... \
  --set executor.defaults.resources.cpu=250m \
  --set executor.defaults.resources.memory=256Mi \
  --set 'config.trustedProxies={<AWS-ingress-CIDR>}'
```
- **QoS PASS:** a task declaring no resources shows
  `kubectl get pod <task> -o jsonpath='{.status.qosClass}'` = **`Guaranteed`**
  (not `BestEffort`); both `requests` and `limits` present. Drive a node to memory
  pressure → the task pod is **not** first evicted.
- **ClientIP PASS:** behind the ALB/NLB, several bad logins from **different**
  clients do **not** share one lockout bucket once `config.trustedProxies` is set
  to the ingress CIDR (before the fix, all requests collapsed to the ingress IP and
  a handful of bad logins locked out everyone).

---

## §5 EKS ↔ GKE deltas (so a GKE pass doesn't give false confidence)

- **Identity:** EKS **IRSA** (`eks.amazonaws.com/role-arn`) vs GKE **Workload
  Identity** (`iam.gke.io/gcp-service-account`). Annotate `serviceAccount` (log
  sink to S3) and `taskServiceAccount` (task cloud access).
- **CNI:** AWS VPC CNI enforces NetworkPolicy differently from GKE Dataplane V2 /
  Calico; kindnet (CI) enforces nothing. Re-check any netpol-dependent path here.
- **Storage:** RWO = EBS; **RWX = EFS** (needed when `replicaCount>1` with
  `logs.persistence.accessMode=ReadWriteMany`). GKE uses PD / Filestore.
- **Ingress:** ALB (IP vs instance target mode) / NLB (proxy-protocol) change what
  `ClientIP` sees → the §4.3 `trustedProxies` CIDR is cloud-specific.

---

## §6 Results + reporting

| # | Check | PASS/FAIL | Notes / evidence |
|---|---|---|---|
| §2 | smoke: task→success | | |
| #722 | audit rows written | | |
| #723 | retry not wedged (§4.2) | | |
| #724 | validation → 400 | | |
| #725 | QoS Guaranteed + ClientIP (§4.3) | | |
| #726 | api has no tls.key (§4.1) | | |
| #727 | migrate job no SA token | | |
| #728 | warm TMPDIR fresh (§4.2) | | |
| #729 | managed-PG re-extract (Lite host) | | |
| — | task pods non-root by default | | |

For each FAIL: open an issue on `neochaotic/leoflow` with the root cause and, where
possible, the file:line (the #722–#729 batch is the quality bar). A red RC →
fix → **rc.4** (tags are immutable, ADR 0033); a green RC → the GA promotion is a
separate maintainer decision.
