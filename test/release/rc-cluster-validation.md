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
#722–#729 batch). For the **next** RC, keep §0/§2/§4/§5 as-is and refresh only
**§3a** (the "features this RC shipped") from that release's CHANGELOG
`[Unreleased]` / release notes. **§3b carries forward** — those rows stand until
the issue they name closes.

---

## Run header (fill in)

| Field | Value |
|---|---|
| RC under test | `<tag>` |
| Cloud / cluster | `<AWS EKS / GCP GKE>` (context: `<kubectl-context>`) |
| CNI | `<AWS VPC CNI / GKE Dataplane V2 / Calico>` — note it, §5 depends on it |
| Node provisioning | (managed node group / Karpenter / GKE node pool?) |
| GKE mode | `<Standard / Autopilot>` — Autopilot mutates pod specs; see §5 |
| Server image | `ghcr.io/neochaotic/leoflow-server:<tag>` |
| Date / operator | `<date>` / `<who>` |

Links: release <https://github.com/neochaotic/leoflow/releases/tag/TAG> ·
Helm guide <https://neochaotic.github.io/leoflow/operate/helm-chart/> ·
chart README (full values) <https://github.com/neochaotic/leoflow/blob/main/helm/leoflow/README.md> ·
install page <https://neochaotic.github.io/leoflow/get-started/installation/>.

---

## §0 Prerequisites

- `kubectl` pointed at the target cluster (EKS / GKE Standard / other); `helm` v3.
- **External Postgres + Redis** reachable from the cluster (the chart runs on any
  cluster with external datastores — see the chart README "datastore
  compatibility" for the exact secret/URL value keys; do not guess them).
- A container registry the cluster can pull from for **your DAG images**
  (ECR / Artifact Registry).
  The control-plane image comes from `ghcr.io/neochaotic/leoflow-server:<rc>`.
- **Cloud identity** for keyless auth, to annotate the ServiceAccounts: EKS
  **IRSA** (`eks.amazonaws.com/role-arn`) or GKE **Workload Identity**
  (`iam.gke.io/gcp-service-account`) — see §4.2/#728 and §5.
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
  -f <your-values>.yaml   # e.g. values.eks.yaml / values.gke.yaml
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

## §3 v0.4.5 feature validation

**§3a is refreshed per RC** from that release's CHANGELOG `[Unreleased]`.
**§3b is not** — those rows stand until the issue they name closes, and carry
forward across RCs. Deleting a §3b row is a decision, not a refresh.

### §3a — the v0.4.5 tranche

✔ = also unit/e2e-verified; ★ = **only a real cluster proves it well**.

Every item below is one of the five GA blockers this release closed, and they
share a root: the hybrid DAG — a `dag.py` with dbt projects as task groups — was
never exercised end to end by CI, so four defects shipped green inside one e2e
fixture that hand-wrote its Dockerfile and used Bash tasks. `dbt-mixing-e2e.sh`
now exercises the generated path, which is why most rows are ✔. What remains is
what a pod does that a container on a laptop does not.

**Task pods are `restartPolicy: Never` and live seconds.** `kubectl exec` into
one is not runnable — the container is gone before you can attach. Where a row
needs to look inside the image *as the cluster pulled it*, use a throwaway pod:

```bash
kubectl run img-probe -n <taskNamespace> --rm -i --restart=Never \
  --image=<dag-image> --command -- sh -c '<command>'
```

- **#17 authoring package in the task image ✔** — deploy a hybrid DAG whose
  `dag.py` starts `from leoflow import dbt_group` and whose Python tasks sit
  either side of the group. **PASS:** the `python` tasks reach `success`. The
  runtime re-imports `dag.py` per task, so a missing package fails every one of
  them with `ModuleNotFoundError`. No ★: `leoflow` ships **in the wheel**
  (`runtime/python/pyproject.toml` `packages = ["leoflow_runtime", "leoflow"]`,
  installed to site-packages by `runtime/Dockerfile`), so it resolves
  independently of `PYTHONPATH` and of UID. `PYTHONPATH=/home/leoflow` is what
  makes *`dag.py`* importable, not `leoflow`. The e2e already runs this with
  `-e PYTHONPATH=` empty, which is stricter than a pod.
- **#20 dbt_groups project baked into the image ✔** — same DAG. **PASS:** the
  `transform__*` tasks reach `success` rather than exiting within seconds. To
  see the image content, use the `img-probe` pod above with
  `ls -l /home/leoflow/<project>/dbt_project.yml`.
  **Second shape, not covered by the e2e:** a group whose `project` is `"."`
  (the DAG dir itself) takes a different `COPY . /home/leoflow/` branch in
  `compile_build.go` and omits `--project-dir` entirely. Run one.
- **#993 absolute host path in the entrypoint ✔★** — **single machine; a second
  one adds nothing.** `dbtProjectDir` only absolutizes when `local` is true, and
  the only production site setting it is `dev.go` (Lite subprocess) — `compile`,
  `deploy` and `deploy --skip-build` all leave it false. So one machine's output
  either carries a host path or it does not.
  **PASS (local; `internal/cli/dbt_projectdir_test.go` asserts the same shape as a unit test):**

  ```bash
  leoflow compile <dag-dir> -o /tmp/spec.json   # no --build
  jq -e '[.tasks[] | .entrypoint // "" | test("--(project|profiles)-dir /") | not] | all' /tmp/spec.json
  ```

  **PASS (cluster) — read the entrypoint the *server* holds, which is what the
  pod fetches over gRPC:**

  ```bash
  API=<control-plane base URL>          # e.g. http://localhost:8080 via port-forward
  TOKEN=$(leoflow auth create-token --server "$API" \
                  --username <u> --password <p>)
  DAG=<the hybrid DAG from #17>         # NOT the §2 smoke DAG — it has no dbt tasks,
                                        # so it passes this vacuously
  curl -fsS -H "Authorization: Bearer $TOKEN" "$API/api/v2/dags/$DAG/spec" \
    | jq -e '[.tasks[] | .entrypoint // "" | test("--(project|profiles)-dir /") | not] | all'
  ```

  **Do NOT** read `kubectl get pod <task> -o jsonpath='{.spec.containers[0].args}'`.
  `BuildPod` sets neither `command` nor `args` — the entrypoint never reaches the
  pod spec — so that jsonpath returns empty on a fixed build *and* on a broken
  one. It is a row that cannot fail.
  **FAIL** is a dbt task exiting seconds after start with "project directory does
  not exist".
- **#994 project-baked profiles.yml ✔** — a group with **no** `connection:` whose
  project ships its own `profiles.yml`. **PASS:** the dbt tasks succeed **and**
  the `/spec` entrypoint for every `transform__*` task carries
  `--profiles-dir <project>`; without the positive assertion you cannot tell the
  fix from a project that never needed it. No ★: the `/tmp` emptyDir is created
  by `mountWritableTmp` **only** when `readOnlyRootFilesystem` is on, which is
  off by default — `/tmp` here is the container's writable layer, identical on
  k3d and GKE.
  **Second arm, not covered anywhere:** a group **with** `connection:` still
  succeeding. `decorateCommands` appends `--profiles-dir` only in the
  no-connection branch, so the fix sits one `switch` arm from the managed-secret
  path — the arm that also crosses ADR 0060 external secrets. Run one.
- **#852 read-only project, for real** — the half no test covers.

  ```bash
  kubectl label ns <taskNamespace> pod-security.kubernetes.io/enforce=restricted
  helm upgrade leoflow ... --set taskPodSecurity.readOnlyRootFilesystem=true
  ```

  **PASS:** the pod is **admitted** by a `restricted`-enforcing namespace and
  reaches `Running` (not `CreateContainerConfigError`), and the dbt tasks succeed
  — which is the observable form of "every write landed in the `/tmp` emptyDir".
  Do not try to observe "nothing was written under `/home/leoflow/<project>`":
  with a read-only rootfs the kernel makes it impossible, and the pod is gone
  before you could look.
  Note the executor never sets `runAsUser` — only `runAsNonRoot` — so the kubelet
  must resolve the image's numeric USER. That half is **already green three ways**
  (every k3d e2e pod, plus the image-level UID assertion in the e2e); it is not
  what makes this row cluster-only.
  **This row has no cloud-only half, and three review rounds failed to find one.**
  Each proposed anchor collapsed on inspection: the kubelet resolving the image's
  numeric USER is already green three ways; PSA `restricted` admission and
  `LimitRange` are in-tree apiserver features k3d enforces identically; node-level
  ephemeral-storage eviction runs on a real kubelet on k3d too. GKE Autopilot
  would be a genuine delta but the RC target is EKS, where it does not exist.
  So: **run it on whatever cluster you have and record the result, but do not
  spend cluster time you would not otherwise spend on it.** The right home for
  this is a k3d e2e setting
  `LEOFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM=true` against a
  `restricted`-labelled namespace, filed as #1016. One thing worth noting while
  you are here, because nothing in our code bounds it: the executor creates that
  emptyDir with **no `sizeLimit`**, so a namespace `LimitRange` or the node's own
  eviction threshold is the only limit on `/tmp/leoflow/dbt/target`.
- **#1005 does a task pod reuse the build-time parse?** — **run this locally, not
  on the cluster.** 

  ```bash
  docker run --rm --entrypoint dbt <dag-image> --debug parse \
    --project-dir <project> --profiles-dir <project>
  ```

  `--project-dir` is always required — the image's `WORKDIR` is `/home/leoflow`
  while the project sits at `/home/leoflow/<project>`, so a bare `parse` dies
  with "Not a dbt project". `--profiles-dir` is required only for a project that
  **bakes its own `profiles.yml`** (the #994 shape); a `connection:` group has no
  baked profile, since the managed one is written at runtime under `/tmp`.

  **Expect "full parse", and treat a partial one as the surprise.**
  `DBT_TARGET_PATH` is `/tmp/leoflow/dbt/target`, empty in a fresh container, so
  `partial_parse.msgpack` cannot be there. This is a question, not a gate — if it
  answers as expected, close #1005 and leave the claim out of the docs. The base image points
  `DBT_TARGET_PATH` at `/tmp/leoflow/dbt/target` while the project (with any
  baked `target/`) is copied to `/home/leoflow/<project>`, and nothing copies one
  to the other — so a full parse per task is the expected answer. Two files and a
  `docker run` settle it; it does not deserve cluster minutes.
- **#15 strict `leoflow.yaml` keys ✔** — unit-covered, no cluster needed. Worth
  one smoke: a project carrying a stray top-level key must fail **both**
  `leoflow validate` and `leoflow compile`, naming the key — and must **not**
  break Lite discovery, which decodes leniently on purpose.

### §3b — standing assertions (carry forward until the issue closes)

- **#800 zero-declaration blind spot** — the row above validates the population
  that *does* warn, so a green RC has been certifying past the one that does not.
  With `secretScoping=permissive` (default), run a DAG declaring **no** variables
  and **no** connections against a tenant that has several connections defined.
  **PASS (today's behavior, and the blind spot made executable):** the task
  receives **every** connection in the vault — observe this with a probe task that
  prints the `AIRFLOW_CONN_*` **names** it was handed, never their values — and
  `GET /api/v2/eventLogs` shows
  **no** `secret.scope_warning` row for that run. That silence is exactly what an
  operator would read as "safe to flip to `enforce`", while this same DAG would
  receive **nothing** after the flip. Until #800's code half lands, this row is
  the reminder that a clean trail is not evidence; when it lands, this row
  inverts — the run must then produce a warning naming zero declarations.
- **#800 stale-declaration blind spot** — the second population the trail never
  shows, and the likelier one in practice, since secret rotation produces it. The
  warning counts only declared names that actually *resolve*, so a DAG whose
  declarations all point at names since deleted from the vault collapses to zero
  declared and never warns — registration-time validation (#724) guards the
  moment of registration, not later deletion. With `secretScoping=permissive`,
  register a DAG declaring `conn_a`, run it, then delete `conn_a` from the vault
  and run it again. **PASS (today's behavior):** the second run receives **every**
  connection in the vault and `GET /api/v2/eventLogs` shows **no**
  `secret.scope_warning` row for it. Flip that same DAG to `enforce` and confirm
  it receives **nothing** — and that no audit row explains why.
- **#722 secret-audit ✔** — with `secretLivenessMode=observe`, run a DAG that
  declares fewer secrets than the vault. **PASS:** `GET /api/v2/eventLogs` shows a
  `secret.scope_warning` row (not just a log line). Flip a scenario to `enforce`
  and confirm a `secret.liveness_denied` row is written too. (This is the row the
  two #800 rows above exist to qualify: it validates the population that *does*
  warn.)
- **#724 validation 400 ✔** — register a DAG version declaring an unknown
  connection. **PASS:** API returns **400** (not 500); message points at
  `leoflow connections set`.
- **#727 migration-job SA token ✔** — `kubectl get job <migrate> -o yaml`.
  **PASS:** `spec.template.spec.automountServiceAccountToken: false`.
- **#729 managed-PG idempotent** — Lite/local, not a cloud cluster. Run the opt-in
  E2E `test/e2e/lite-managed-pg-reextract.sh` on a networked host with managed-PG.
- **task pods non-root by default** — the rc.2 behavior change, still standing.
  `kubectl get pod <task> -o jsonpath='{.spec.securityContext}'`. Root-assuming
  DAG images fail (expected).

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

### §4.5 — HA posture: the PDB that appears on `helm upgrade`, and restart recovery

Two `[Unreleased]` changes with no other executable statement in this file.

**PDB (changes existing installs on upgrade).** The budget now renders
automatically whenever the guaranteed replica floor is `> 1` — every
`split.enabled` install (api default 2) and every `autoscaling.enabled` install
(`minReplicas` default 2). A budget that appears unannounced is what hangs node
drains and stalls auto-upgrades.

`--set split.enabled=true` alone **will not render** on §1's baseline: the chart
refuses more than one control-plane pod against a ReadWriteOnce logs PVC. Turn
the PVC off (and ship logs to object storage), or layer the HA profile over your
own values — never apply `values-ha.yaml` alone, it carries `CHANGEME`
placeholders for the database, Redis and secrets that would repoint the control
plane at a nonexistent Postgres:

```bash
helm upgrade leoflow ... -f <your-values>.yaml -f helm/leoflow/examples/values-ha.yaml
kubectl get pdb -n <ns> -l app.kubernetes.io/instance=leoflow
```

**PASS:** a PDB now exists that you never asked for. That object's existence is
the whole observation — it is what flips if the change is reverted.

**The drain is a separate, weaker check.** A drain completes with no PDB at all,
and faster, so "the drain completed" does not discriminate. It is worth running
only to catch the budget *blocking* one, and it needs a precondition or it reads
FAIL on a correct install — the chart ships no default anti-affinity, and
`values-ha.yaml` spreads with `whenUnsatisfiable: ScheduleAnyway`, which is
best-effort and can still bin-pack both replicas onto one node:

```bash
kubectl get pods -n <ns> -l app.kubernetes.io/instance=leoflow -o wide   # two distinct NODEs
kubectl drain <node> --ignore-daemonsets --delete-emptydir-data
```

**Not worth running: the `unhealthyPodEvictionPolicy` field check.**
`AlwaysAllow` is the chart default and only an apiserver older than 1.27 prunes
it — neither EKS nor GKE will sell you one, so the check prints `AlwaysAllow`
unconditionally. The observation that *would* flip is the behavior: break **both**
replicas (bad DB credential or a bogus image), then drain. Under `AlwaysAllow`
the eviction succeeds; under `IfHealthyBudget` it blocks on an already-down
control plane. Stage that if you want the row, and skip it otherwise.

The chart **fails the render** if `minAvailable` and `maxUnavailable` are both
set — worth one deliberate attempt, since the point of that guard is to fail at
render rather than at install.

**Restart recovery (ADR 0052) — do NOT hand-roll this.** Killing the control
plane mid-task and watching the task settle to `success` proves nothing: the
agent's terminal report retries for as long as the control plane is unreachable,
so the ordinary report path produces the same green without the recovery path
ever running. `test/e2e/chaos-runtime.sh` already covers it on k3d, and refuses
that exact false green — it bakes `LEOFLOW_CHAOS_DIE_BEFORE_REPORT` into the DAG
image (dispatch strips author-declared `LEOFLOW_`-prefixed keys, #828/#829) and
fails loudly if the pod ends `Completed`, because that means the fault seam never
fired. Run it rather than improvising:

```bash
LEOFLOW_CHAOS_ONLY=CD test/e2e/chaos-runtime.sh
```

**PASS:** scenarios C and D both pass. Two caveats worth stating, because they
bound what a green here means:

- The script is **not in any workflow** — only `make chaos-runtime`, which
  nothing invokes. An RC is the moment it actually gets run, which is why it is
  named here at all.
- It runs the control plane as **host processes** from `bin/leoflow-server` on a
  k3d cluster it creates and deletes. So it validates **the tree at the RC tag**,
  not `ghcr.io/neochaotic/leoflow-server:<rc>`. Check out the tag and build
  before running it, and record that the published image is not what was
  exercised.

Prerequisites beyond §0: a Linux docker host (a Lima VM on macOS), `k3d`, `jq`,
a migrated external Postgres, and **`bin/leoflow` + `bin/leoflow-server` already
built** — the script builds the base image but not those two.

### §4.6 — #1023: readiness fails when the schema disappears under a running pod

The v0.4.5 RC found this the expensive way: an ephemeral-storage Postgres came
back **empty** after a node recycle and the running control plane kept answering
`/readyz` 200 while the scheduler could not read `dag_runs`. A Ping proves the
connection, not the schema, so nothing in the probe noticed.

It has two halves, and **both** have to be run. The first is the fix. The second
is the outage the fix can cause if it is built naively, and it is the more
expensive of the two to discover in production.

#### §4.6a — a schema that vanishes takes the pod out of rotation

Do this against a **throwaway** database — it drops the schema.

**Port-forward to the POD, not the Service.** This is the whole point of the
check: a correct build leaves the Service's endpoints within ~30s, and a
port-forward through the Service dies with it. `curl` then prints `000` — a
connection failure, not an HTTP status — and `000` is not `503`, so the row
would read FAIL against a build that is working exactly as intended. Forwarding
to the pod keeps talking to it after it is out of rotation, which is also the
only way to see the 503 body at all.

```bash
NS=<ns>
POD=$(kubectl get pod -n "$NS" -l app.kubernetes.io/instance=leoflow -o name | head -1)
kubectl port-forward -n "$NS" "$POD" 8080:8080 &          # 9090 in the scheduler-only role
PF=$!

psql "$DATABASE_URL" -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;'
```

Readiness is `periodSeconds: 10 × failureThreshold: 3`, so the flip takes up to
30s. Wait for the condition rather than sampling it — checked instantly, a
**correctly fixed** build still shows `READY 1/1`, and the row cannot fail:

```bash
kubectl wait --for=condition=Ready=false --timeout=60s -n "$NS" "$POD"
curl -sS -o /dev/stderr -w '%{http_code}\n' localhost:8080/readyz    # expect 503
kubectl get endpoints -n "$NS" -l app.kubernetes.io/instance=leoflow  # pod's IP gone
```

**PASS:** `kubectl wait` returns success (it did not time out), `/readyz` answers
**503**, the pod's IP is no longer in the Service's endpoints, and the 503 body
names only the dependency — **no DSN, credentials or internal hostname**, since
the endpoint is unauthenticated. The version detail is in the pod log.

Read the detail string, do not just grep for 503. `postgres schema not current`
is the schema verdict; `postgres unavailable` is a database that could not be
**read** — a timeout, a reset connection, an exhausted pool. Both are 503 and
only the first one is this check. A run that shows `postgres unavailable` here
has proved something about the pool, not about #1023.

**Liveness must NOT fire, and this is the half that needs patience.** Liveness is
`periodSeconds: 20 × failureThreshold: 3`, so a restart needs **60s of continuous
failure**. `RESTARTS` read immediately after the DROP is `0` whether liveness is
schema-blind or not, so the instant check proves nothing. Hold the broken state
for ~90s and assert `/healthz` stays 200 the whole time — a positive assertion,
not the absence of a restart:

```bash
end=$((SECONDS+90))
while [ $SECONDS -lt $end ]; do
  printf '%s ' "$(curl -sS -o /dev/null -w '%{http_code}' localhost:8080/healthz)"
  sleep 10
done; echo
kubectl get pod -n "$NS" "$POD" -o jsonpath='{.status.containerStatuses[0].restartCount}{"\n"}'
```

**PASS:** every sample is `200` and the restart count is unchanged from before
the DROP. A `CrashLoopBackOff`, or any 5xx from `/healthz`, is a FAIL — liveness
is deliberately schema-blind, because restarting creates no schema and a crash
loop would cost you the pod you need to diagnose.

**Recovery — two traps, both of which have cost time.**

*The Job is not there to re-run.* `migration-job.yaml` carries
`helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded`, so after a
successful install the Job object is **deleted**. `kubectl create job
--from=job/leoflow-migrate` answers `NotFound`. What re-runs the migration is a
Helm operation that fires the `pre-upgrade` hook:

```bash
helm upgrade leoflow <chart> -n "$NS" --reuse-values --wait
```

*`CREATE SCHEMA public` does not restore who may use it.* The recreated schema is
owned by whichever role ran the `psql`, and every grant that was on the old one
went with it. If the migrate Job connects as a different role — the usual split,
and mandatory under the read-only `role:api` identity (ADR 0049) — it fails on
`permission denied for schema public` and the Job's `backoffLimit: 3` turns that
into a failed `helm upgrade` several minutes later. Restore ownership first,
substituting your own roles:

```sql
ALTER SCHEMA public OWNER TO <migrate role>;
GRANT USAGE, CREATE ON SCHEMA public TO <migrate role>;
GRANT USAGE ON SCHEMA public TO <api role>;   -- only if you run the split identity
```

**PASS:** `/readyz` returns to 200 and the pod re-enters the endpoints **without
a restart** — the check is per-probe, so recovery needs no pod churn. Note that
`--wait` now blocks on real readiness, which is the #1023 fix seen from the other
side: before it, `helm upgrade --wait` reported success against this database.

#### §4.6b — a rolling upgrade never drops the Service to zero endpoints

The reason §4.6a is not the whole check. The migrate Job is a
`pre-install,pre-upgrade` hook, so on **upgrade** it runs while the OLD pods are
live and serving, and golang-migrate commits `dirty=true` for the duration of
each migration body. A readiness probe that treats `dirty` as not-ready flips
**every** old replica at the same instant — they all read the same one row — for
any migration longer than `3 × 10s`. The Service reaches zero endpoints, and in
the `all` role that Service also carries gRPC, so running task pods lose the
control plane mid-upgrade. Readiness is version-aware on `dirty` precisely to
avoid this, and the property is not observable in §4.6a.

Needs a migration slow enough to span more than 30s of probing. Simulate it by
holding the row that every replica reads, in a transaction, while an upgrade runs:

```bash
# terminal 1 — watch the endpoints for the whole upgrade; never let this reach 0
kubectl get endpoints -n "$NS" leoflow -w

# terminal 2 — park the schema in the state a long migration produces:
#   dirty, at a version ABOVE what the running binaries embed
psql "$DATABASE_URL" -c \
  'UPDATE schema_migrations SET version = version + 1, dirty = true;'
sleep 60
psql "$DATABASE_URL" -c \
  'UPDATE schema_migrations SET version = version - 1, dirty = false;'
```

**PASS:** across the whole 60s the endpoints list never empties, `/readyz` on a
running pod stays **200**, and the pod log carries **one** line about a migration
being in flight — not one per probe period. If the endpoints drop to zero, or
`/readyz` goes 503 here, readiness is treating an in-flight forward migration as
a broken schema and every `helm upgrade` with a slow migration is an outage.

**The other arm, and it must FAIL closed.** The same state at or *below* the
running binary's own version is a genuinely half-applied schema it depends on,
and must take the pod out of rotation:

```bash
psql "$DATABASE_URL" -c 'UPDATE schema_migrations SET dirty = true;'   # same version
kubectl wait --for=condition=Ready=false --timeout=60s -n "$NS" "$POD"
psql "$DATABASE_URL" -c 'UPDATE schema_migrations SET dirty = false;'
```

**PASS:** the pod goes NotReady here. A build that stays ready through **both**
arms has not fixed #1023, it has disabled the dirty check.

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
- **GKE Autopilot vs Standard:** Autopilot mutates pod specs, injects resource
  requests and constrains securityContext / ephemeral storage — which lands
  directly on §3a/#852 (read-only rootfs + an unsized emptyDir) and on §4.3's QoS
  row. This runbook assumes **Standard**; record the mode in the run header.
  *Confirm this against your own cluster before trusting it — it is the one delta
  here not verified against the code.*

---

## §5b What a GKE run cannot settle

No §3a row **depends** on a §5 delta — image content, entrypoint strings,
container security context and YAML strictness have no cloud delta. (#852 names
Autopilot only to rule it out as a justification; it does not rest on it.) The
rows that do depend on a delta are all in §4, and a GKE pass on these is not an
EKS pass:

- **§4.2 / #728 — IRSA credential cache.** "creds cached to `~/.aws/cli/cache`"
  has no Workload Identity equivalent; that half is unrunnable on GKE. The
  `$TMPDIR` sentinel half does transfer. Split the row: **NOT VERIFIED (cloud)**
  for the cache.
- **§4.3 / #725 — ClientIP.** GCLB's `X-Forwarded-For` shape differs from
  ALB/NLB, and the `trustedProxies` CIDR does not transfer. Run it for the code
  path; record **NOT VERIFIED (cloud)** for the EKS claim.
- **§4.4 / #804 — containment.** This is the **most misleading row to pass on
  GKE**, because the delta runs the wrong way: Dataplane V2 enforces
  NetworkPolicy natively, so the metadata-egress test passes almost by default,
  while the AWS VPC CNI enforces *nothing* unless its network-policy agent is on.
  A green here is the weakest evidence in the file. Record **NOT VERIFIED
  (cloud)** for EKS and re-run on the EKS RC with the addon flag in the header.

---

## §6 Results + reporting

Record **NOT VERIFIED (cloud)** rather than PASS for anything §5 says this cloud
cannot settle. A row left blank reads as "not run"; a row marked PASS on the wrong
cloud reads as proof, and that is the failure §5 exists to prevent.

| # | Check | PASS/FAIL | Notes / evidence |
|---|---|---|---|
| §2 | smoke: task→success | | |
| #17 | hybrid: python tasks succeed (§3a) | | |
| #20 | dbt_groups project present in the image, both shapes (§3a) | | |
| #993 | bare compile + /spec: no host path in any entrypoint (§3a) | | |
| #994 | no-connection group succeeds AND carries --profiles-dir (§3a) | | |
| #994 | second arm: a group **with** `connection:` still succeeds (§3a) | | |
| #852 | read-only rootfs: pod admitted, dbt tasks succeed (§3a; no cloud delta) | | |
| #1005 | `docker run … dbt --debug parse`: full or partial? (local, §3a — a question, not a gate) | | |
| #15 | stray top-level key fails validate AND compile; Lite discovery unaffected (§3a) | | |
| #800 | zero-declaration: no scope_warning, task gets every connection (§3b) | | |
| #800 | stale-declaration: deleted conn → no warning; enforce → nothing (§3b) | | |
| #722 | audit rows written | | |
| #723 | retry not wedged (§4.2) | | |
| #724 | validation → 400 | | |
| #725 | QoS Guaranteed + ClientIP (§4.3) | | |
| #802 | partial resource pair → Burstable + boot WARN + compile error (§4.3) | | |
| #804 | control-plane policy on, task policy off → no policy **selects** a task pod (§4.4) | | |
| #726 | api has no tls.key (§4.1) | | |
| #727 | migrate job no SA token | | |
| #728 | warm TMPDIR fresh (§4.2) | | |
| #729 | managed-PG re-extract (Lite host) | | |
| — | task pods non-root by default | | |
| HA | HA-profile upgrade: a PDB exists that nobody asked for (§4.5) | | |
| #1023 | schema dropped under a running pod: `/readyz` 503, pod leaves endpoints, no restart across 90s (§4.6a) | | |
| #1023 | in-flight migration above the running binary: endpoints never empty, `/readyz` stays 200; dirty at the same version still goes NotReady (§4.6b) | | |
| ADR 0052 | `LEOFLOW_CHAOS_ONLY=CD chaos-runtime.sh` — C and D pass (§4.5) | | |

For each FAIL: open an issue on `neochaotic/leoflow` with the root cause and, where
possible, the file:line (the #722–#729 batch is the quality bar). A red RC →
fix → **rc.4** (tags are immutable, ADR 0033); a green RC → the GA promotion is a
separate maintainer decision.
