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
| RC under test | `<tag>` |
| Cloud / cluster | `<AWS EKS / GCP GKE>` (context: `<kubectl-context>`) |
| CNI | `<AWS VPC CNI / GKE Dataplane V2 / Calico>` — note it, §5 depends on it |
| Node provisioning | (managed node group / Karpenter / GKE node pool?) |
| Server image | `ghcr.io/neochaotic/leoflow-server:<tag>` |
| Date / operator | `<date>` / `<who>` |

Links: release <https://github.com/neochaotic/leoflow/releases/tag/TAG> ·
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
  `LEOFLOW_VERSION=<tag> curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/install.sh | sh`
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
- `auth.agentTokenTransport` (`envvar`) / `auth.secretLivenessMode` (`observe`) /
  `auth.secretScoping` (`permissive`) — the last one became a chart value in #803;
  before that it was reachable only through `extraEnv`.
- `execution.warmPoolsEnabled` (false) — requires `agentTokenTransport=exchange`
  **and** `secretLivenessMode=enforce` (chart refuses to render otherwise).
- `config.trustedProxies` (`[]`) — **#725**, must be reachable now.
- `executor.defaults.resources.cpu` / `.memory` (`""`) — **#725**, QoS defaults;
  set **both or neither** (**#802**: one alone is Burstable and suppresses the
  other dimension entirely).
- `networkPolicy.enabled` (`false`) / `taskNetworkPolicy.enabled` (`false`) —
  **#804**, two independent policies; the second is the task-pod containment.
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

## §3 v0.4.5 feature validation (refresh this section per RC)

The v0.4.5 tranche. ✔ = also unit/e2e-verified; ★ = **only a real cluster proves it well**.

Every item below is one of the five GA blockers this release closed, and they
share a root: the hybrid DAG — a `dag.py` with dbt projects as task groups — was
never exercised end to end by CI, so four defects shipped green inside one e2e
fixture that hand-wrote its Dockerfile and used Bash tasks. `dbt-mixing-e2e.sh`
now exercises the generated path, which is why most rows are ✔. What remains is
what a pod does that a container on a laptop does not.

- **#17 authoring package in the task image ✔★** — deploy a hybrid DAG whose
  `dag.py` starts `from leoflow import dbt_group` and whose Python tasks sit
  either side of the group. **PASS:** the `python` tasks reach `success`. The
  runtime re-imports `dag.py` per task, so a missing package fails every one of
  them with `ModuleNotFoundError`. ★ because the e2e proves the image; only a pod
  proves the agent's own import path under `PYTHONPATH=/home/leoflow` and UID
  65532.
- **#20 dbt_groups project baked into the image ✔** — same DAG. **PASS:** the
  `transform__*` tasks reach `success` rather than exiting within seconds.
  `kubectl exec` into a task pod and confirm `/home/leoflow/<project>/dbt_project.yml`
  exists.
- **#993 absolute host path in the entrypoint ★** — the one the e2e structurally
  cannot reach, because it needs two machines. Compile on machine A, then
  `leoflow deploy --skip-build` from a CI runner or a second machine where that
  path does not exist. **PASS:** `kubectl get pod <task> -o jsonpath='{.spec.containers[0].args}'`
  contains no `/Users/` or `/home/<someone>/` prefix. **FAIL** is a dbt task
  exiting seconds after start with "project directory does not exist".
- **#994 project-baked profiles.yml ✔★** — a group with **no** `connection:`
  whose project ships its own `profiles.yml`. **PASS:** the dbt tasks succeed.
  ★ because the base image points `DBT_PROFILES_DIR` at `/tmp`, and only a pod
  with a real `/tmp` emptyDir shows whether the read path and the write path
  agree.
- **#852 read-only project, for real ★** — the half no test covers.
  `taskPodSecurity.readOnlyRootFilesystem=true` with a `/tmp` emptyDir, and
  `runAsNonRoot=true` with **no** `runAsUser`. **PASS:** the pod reaches `Running`
  (the kubelet resolves the image's numeric USER — a different code path from
  `docker run`, and a symbolic `USER nonroot` fails here where it passes locally),
  the dbt tasks succeed, and nothing is written under `/home/leoflow/<project>`.
  Watch for a namespace-default emptyDir `sizeLimit` truncating
  `/tmp/leoflow/dbt/target` on a large manifest — every parse artifact lands there
  now.
- **#1005 does a task pod reuse the build-time parse? ★** — twenty minutes, and it
  settles a claim removed from the docs rather than left standing. Run one dbt
  task with `dbt --debug` and read whether it reports a full parse or a partial
  one. Nothing in the tree copies the baked `target/` to `DBT_TARGET_PATH`, so a
  full parse per task is the expected answer; if so, close #1005 and leave the
  claim out.
- **#15 strict `leoflow.yaml` keys ✔** — unit-covered, no cluster needed. Worth
  one smoke: `leoflow validate` against a project carrying a stray top-level key
  must fail and name it.

### Not provable on GKE

The four §5 deltas. Record them as **not verified on this cloud** rather than
PASS — a GKE run that reports them green is the false confidence §5 exists to
prevent. Specifically for this tranche: nothing above depends on IRSA, RWX or
ingress, but **NetworkPolicy enforcement does differ**, so any netpol-dependent
row is GKE-shaped evidence only.


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
- **PARTIAL-pair PASS (#802):** this section only ever set both fields, so it
  certified past the partial case entirely. Re-run with **cpu only**:
  ```bash
  helm upgrade leoflow ... --set executor.defaults.resources.cpu=250m \
    --set executor.defaults.resources.memory=""
  ```
  **PASS** is all three: the control-plane log carries one boot `WARN` with
  `config_key=executor.defaults.resources_cpu` and
  `missing_config_key=executor.defaults.resources_memory`; a task declaring no
  resources shows `qosClass` = **`Burstable`**, *not* the `Guaranteed` the chart
  comment used to promise; and `kubectl get pod <task> -o jsonpath='{.spec.containers[0].resources}'`
  shows **no memory request or limit at all**. Also compile a `leoflow.yaml` whose
  `defaults.resources` sets only `cpu` — **PASS:** `leoflow compile` **fails**
  naming the missing field (`missing property 'memory'`), it does not produce a
  `dag.json`.
- **ClientIP PASS:** behind the ALB/NLB, several bad logins from **different**
  clients do **not** share one lockout bucket once `config.trustedProxies` is set
  to the ingress CIDR (before the fix, all requests collapsed to the ingress IP and
  a handful of bad logins locked out everyone).

### §4.4 — #804: the two NetworkPolicy values are not one value

```bash
helm upgrade leoflow ... --set networkPolicy.enabled=true
```
- **Separation PASS:** with the control-plane policy ON and
  `taskNetworkPolicy.enabled` left at its `false` default, **no policy selects a
  task pod**. Check selection, not object count: `taskNamespace` defaults to
  `leoflow`, which is the release namespace in §1's baseline, so counting objects
  there returns the control-plane policy and reads as a FAIL on a correct
  install.

  ```bash
  kubectl -n <taskNamespace> get netpol -o json | jq '
    [ .items[]
      | select( (.spec.podSelector.matchLabels // {} | keys)
                + [ (.spec.podSelector.matchExpressions // [])[].key ]
                | any(startswith("leoflow.io/")) )
    ] | length'
  # PASS = 0
  ```

  Task pods carry only `leoflow.io/*` labels (`internal/executor/kubernetes.go`),
  while the control-plane policy selects `app.kubernetes.io/name` +
  `instance` — so it cannot select a task pod even in the same namespace. The
  task pods are uncontained, which is exactly what the hardening section used to
  deny. `kubectl -n <releaseNamespace> get networkpolicy -o yaml` shows the
  control-plane policy's `spec.egress` ending in the **empty rule (`{}`)**, i.e.
  allow-all: enabling it restricts ingress only.
- **Containment PASS:** re-run with `--set taskNetworkPolicy.enabled=true` and
  confirm one policy in `<taskNamespace>`, **and that this CNI enforces it** — a
  task pod's `curl` to `169.254.169.254` must time out. On kindnet it will not
  (kindnet enforces nothing), and the AWS VPC CNI enforces only with its
  network-policy agent enabled: a rendered object is not evidence.
- **Keyless caveat PASS:** with the task policy on, a DAG using keyless
  external-secrets auth (GKE Workload Identity / EKS Pod Identity) fails until
  `taskNetworkPolicy.allowMetadataEgress` re-allows that one `/32`. Confirm the
  failure *and* the recovery — this is why the policy stays opt-in.

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

Record **NOT VERIFIED (cloud)** rather than PASS for anything §5 says this cloud
cannot settle. A row left blank reads as "not run"; a row marked PASS on the wrong
cloud reads as proof, and that is the failure §5 exists to prevent.

| # | Check | PASS/FAIL | Notes / evidence |
|---|---|---|---|
| §2 | smoke: task→success | | |
| #17 | hybrid: python tasks succeed (§3) | | |
| #20 | dbt_project.yml present in the task pod (§3) | | |
| #993 | deploy --skip-build: no host path in pod args (§3) | | |
| #994 | group with no connection: dbt tasks succeed (§3) | | |
| #852 | readOnlyRootFilesystem + numeric non-root: pod Running, project unwritten (§3) | | |
| #1005 | task pod: full parse or partial? (§3) | | |
| #722 | audit rows written | | |
| #723 | retry not wedged (§4.2) | | |
| #724 | validation → 400 | | |
| #725 | QoS Guaranteed + ClientIP (§4.3) | | |
| #802 | partial resource pair → Burstable + boot WARN + compile error (§4.3) | | |
| #804 | control-plane policy on, task policy off → 0 netpols in taskNamespace (§4.4) | | |
| #726 | api has no tls.key (§4.1) | | |
| #727 | migrate job no SA token | | |
| #728 | warm TMPDIR fresh (§4.2) | | |
| #729 | managed-PG re-extract (Lite host) | | |
| — | task pods non-root by default | | |

For each FAIL: open an issue on `neochaotic/leoflow` with the root cause and, where
possible, the file:line (the #722–#729 batch is the quality bar). A red RC →
fix → **rc.4** (tags are immutable, ADR 0033); a green RC → the GA promotion is a
separate maintainer decision.
