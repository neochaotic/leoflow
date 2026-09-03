# Changelog

All notable changes to Leoflow are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Control-plane HA as the first-class, guarded posture (Helm chart + docs).** A
  production drill showed the single-replica control plane is evicted by
  autoscaler consolidation as routine bin-packing, and each restart costs tens
  of seconds (image pull + boot + leadership) during which nothing dispatches.
  The chart already supported `replicaCount > 1` (leader election); it now makes
  the safe HA path one switch and guards the unsafe one. `replicaCount > 1` (or
  an HPA / split with more than one mounter) on a `ReadWriteOnce` **or**
  `ReadWriteOncePod` logs PVC now **fails the render** with a message that leads
  with the recommended fix (object-store task logs, `logs.persistence.enabled:
  false` + `logs.sink`) and names the `ReadWriteMany` alternative — previously
  `ReadWriteOncePod` slipped through and the message pointed only at RWX. The
  same mounter ceiling now drives a second refusal: `split.enabled` with
  `logs.persistence.enabled: false` and the `disk` sink fails the render, because
  the scheduler pod would write every task log into its own emptyDir while each
  api pod reads from its own — no log ever readable, silently.
  `podDisruptionBudget.enabled` is now tri-state: unset (the new default) renders
  the PDB **iff** the guaranteed replica floor is above one (`replicaCount`, the
  HPA `minReplicas`, or `split.api.replicaCount`), because a `minAvailable: 1`
  budget over a single replica blocks every voluntary eviction (node drains hang,
  auto-upgrades stall); explicit `true`/`false` still wins. **Upgrade note:** this
  is not only for hand-set `replicaCount > 1` — every `split.enabled` install
  (api default 2) and every `autoscaling.enabled` install (`minReplicas` default
  2) gains a PodDisruptionBudget on `helm upgrade`; the install NOTES call it out,
  and `podDisruptionBudget.enabled: false` opts out. The PDB sets
  `unhealthyPodEvictionPolicy: AlwaysAllow` (new value, `""` omits it for
  apiservers older than 1.27) so two unready replicas never leave a node drain
  hanging on an already-down control plane. NOTES also warn when a PDB is forced
  onto one replica or when more than one pod (HPA ceiling or split included) runs
  on per-pod emptyDir logs. New `topologySpreadConstraints` value (a constraint
  without `labelSelector` gets the Deployment's own selector) and new
  `terminationGracePeriodSeconds` value, both unset by default so a default
  install's pod spec is unchanged; the grace buys headroom for the HTTP shutdown
  and the dispatch-pool drain, not leadership handoff (the lease frees within a
  tick of SIGTERM regardless). New `helm/leoflow/examples/values-ha.yaml`
  profile: two replicas spread across nodes, object-store logs with memory sized
  for the sink's per-attempt buffer, auto PDB, 60s drain grace, and a commented
  EKS/Karpenter-only `karpenter.sh/do-not-disrupt` opt-in. New docs page
  *Control-plane HA and disruption posture* covers the restart window, the
  storage precondition (including what the object sink makes durable and when),
  why the api/scheduler split is not dispatch-HA, the single-replica PDB trap and
  how each platform honors PDBs, and the involuntary disruptions no PDB prevents.
  The shipped default stays `replicaCount: 1`: flipping it would
  Multi-Attach-break every existing install on upgrade.

### Fixed

- **A control-plane restart no longer marks a succeeded task failed, and no
  longer condemns the rest of its run.** A production drill (kill the
  control-plane pod mid-run) turned a dbt task that had SUCCEEDED into `failed`
  and failed the whole run. Five compounding causes, each now fixed:
  - The agent abandoned a terminal report after 6 attempts (~31s) — shorter than
    a control-plane restart — while its heartbeat loop kept going indefinitely.
    The report now retries until its context ends (SIGTERM, pod delete, execution
    timeout), with the pause between attempts capped at the heartbeat interval so
    it reconnects within about one heartbeat plus the gRPC channel's own
    reconnect backoff once the server returns. Credential rejections are still
    returned at once. The RUNNING pre-flight report takes the same path, on
    purpose: an agent that cannot reach the control plane does not start user
    code until it can. Because both reports now outlast an outage, every task
    pod also gets an `activeDeadlineSeconds` floor when its DAG declares no
    execution timeout — derived from `auth.max_attempt_credential_lifetime`,
    past which the pod cannot renew its credential — so no task pod is left
    Running forever after a total control-plane outage. A user-declared timeout
    is never shortened.
  - The pod-lost reaper (every scheduler tick) raced the reconciler (every 30s)
    for a pod that finished during the outage, and won — marking it `pod_lost`
    before the reconciler could recover the pod's durable outcome record. It now
    honors the same post-leadership grace as the agent-lost reaper, so the
    durable outcome is recovered first.
  - A downstream task was persisted `upstream_failed` — a terminal state nothing
    reverts — while its upstream was infra-failed but still re-placeable (parked
    in its re-place backoff). Downstream tasks now wait until an upstream is
    TERMINALLY failed (retries and infra re-place exhausted).
  - Reapers could mark or delete during the SIGTERM drain or a leader step-down.
    Every destructive reaper action is now gated on a live context, no step-down
    in progress, and (when wired) current leadership; the successor redoes the
    reap under its own grace.
  - The timing ladder the recovery depends on is validated at server boot:
    `heartbeat < agent-lost threshold < agent-lost grace < token TTL`, two
    `reconcile interval`s below both post-leadership graces (one is not enough
    to guarantee a completed sweep), the scheduler's longest infra re-place
    delay below the orphan-run threshold, and the token TTL below
    `auth.max_attempt_credential_lifetime`. That last rung is the only one an
    operator can move — hardening the ceiling below the TTL would silently
    disable heartbeat renewal and with it the whole recovery — so its failure
    names the config key; every other rung is a build-time constant and its
    failure asks for a bug report.

- **Connection/variable writes are now tri-state, restoring explicit-clear and
  fixing the masked write-back overwrite (#887, subsumes #874).** The safe-merge
  upsert (v0.4.4) preserved any omitted field, but because the API request body
  used plain strings it could not tell "field omitted" (preserve) from "field
  present and empty" (clear), so `connections set c --login ""` was a silent
  no-op and a masked secret (`***`) read back from a GET and re-submitted — as
  the Admin UI does — overwrote the real secret with the literal mask. The write
  body is now tri-state: an omitted field preserves the stored value, an explicit
  empty string clears it, and a secret field equal to the mask (a password, or
  any key inside `extra` whose value is exactly `***`, or a sensitive-keyed
  variable value) is treated as unchanged and preserved. `leoflow connections set`
  can now clear a field by passing it as an empty string (e.g. `--login ''`)
  instead of "delete and recreate", and re-saving a connection/variable read
  from the API no longer clobbers its unreadable secrets.
- **A control-plane eviction no longer drops the in-flight attempt logs.** Two
  coupled defects made the recommended HA log path (object storage) lose the
  complete log of every attempt running at the moment of an eviction. The object
  sink held a whole attempt in memory and wrote it as one object on close, and
  the shutdown path ran an unbounded gRPC graceful stop that waited on the
  agents' open log streams — held open for the whole task — so with any task
  running the pod burned its entire `terminationGracePeriodSeconds`, was
  `SIGKILL`ed, and the deferred close never ran. Now the object sink rewrites the
  stored object incrementally (whenever 1 MiB is unflushed, or every 5 s while
  anything is, damped for very large logs so a big attempt uploads about nine
  times its size in total rather than quadratically), so a hard kill loses at
  most each attempt's unflushed tail; open log streams are closed and flushed as
  soon as `SIGTERM` arrives (the agent keeps running its task — log shipping is
  best-effort); and the gRPC graceful stop is bounded at 5 s with a forced-stop
  fallback that still lets the remaining handlers finish their flushes, so the
  shutdown completes within the default 30 s grace instead of ending in
  `SIGKILL`. The final flush runs on a context detached from the server's
  lifecycle, which `SIGTERM` cancels at exactly that moment. The disk sink (Lite,
  RWX PVC) is unchanged. Known gap, documented: the agent does not re-open a log
  stream its control plane closed, so lines a task prints after its control
  plane went away are not shipped by that task.

## [0.4.4] - 2026-09-03

### Security

- **Bumped `google.golang.org/grpc` to v1.83.1 (CVE-2026-84304, HIGH).** The
  control plane ↔ agent RPC layer used gRPC-Go v1.83.0, which a HIGH advisory
  covers; v1.83.1 is the upstream fix. `govulncheck` reports the vulnerable
  symbol is not reachable from Leoflow's call graph, so no released build was
  exploitable, but the dependency is patched to clear the advisory outright
  rather than carry a known-fixable HIGH in the RPC layer.

### Added

- **First-class `leoflow connections` and `leoflow variables` CLI groups (#881).**
  The Lite inner loop (and Pro) can now manage connections and variables directly
  — `set` (upsert), `list`, `get`, `delete` — instead of hand-rolling `curl`
  against `/api/v2`; the error for an undeclared connection already pointed at
  `leoflow connections set`, which now exists. Secrets are sent on write but never
  printed back (list/get omit secret columns; the server masks `extra` and
  sensitive-keyed values), and `--password-stdin` / `--extra-file` keep a secret
  off argv and out of shell history. `set` is a **safe merge**: only the fields
  you pass change, so a partial `set --host newhost` no longer wipes the omitted
  (and unreadable) password — the control-plane upsert now preserves any field a
  write omits rather than clobbering it (this also fixes the same latent overwrite
  on the Admin UI and PATCH write paths). To clear a field, delete and recreate.

### Security

- **A Lite dbt task no longer writes its generated `profiles.yml` into the project
  (#12).** In Lite the dbt profile step defaulted its output dir to the process
  CWD — the dbt project in the user's working tree — so the generated
  `profiles.yml`, which carries the managed connection's secret in clear, could
  overwrite the repo's version-controlled `profiles.yml`: one `git add` from
  committing a live credential. The Lite executor now injects the same private
  `DBT_PROFILES_DIR`/`DBT_TARGET_PATH`/`DBT_LOG_PATH` scratch the pod base image
  provides, and the runtime never falls back to the CWD — the profile lands in a
  private per-task dir, never the project.
- **Sensitive values in a connection's `extra` are masked in read API responses
  (#11).** `GET /api/v2/connections` (list and by-id) returned the free-form
  `extra` verbatim, so provider secrets that ride there — OAuth `client_secret`,
  PATs/`token`, `private_key`, BigQuery `keyfile_dict` — were echoed in clear (the
  `password` field was already withheld). Secret-bearing keys are now redacted to
  `***` on read, matching the Variable masking; non-secret keys (host, http_path,
  account, schema, method) are preserved.

### Changed

- **The control-plane Helm defaults no longer let the kubelet kill the server
  under load (#860).** The liveness/readiness probes are now exposed in
  `values.yaml` (`probes.liveness` / `probes.readiness`) and default to a
  forgiving 5s liveness / 3s readiness timeout with `failureThreshold: 3` —
  instead of Kubernetes' implicit 1s/3, which a busy scheduler could miss during a
  task-pod burst and be restarted mid-run (cascading in-flight tasks to
  agent_lost). Default CPU/memory requests are raised to `250m`/`256Mi` so the
  server keeps enough headroom to answer probes while fanning out task pods.
- **The control-plane Deployment update strategy is exposed and defaults safely
  (#868).** A rolling upgrade surges a second pod that Multi-Attach-deadlocks on a
  ReadWriteOnce logs PVC, so an upgrade never converged (operators worked around
  it with an in-cluster patch). `deployment.strategy` is now settable, and when
  unset it defaults to `Recreate` whenever `logs.persistence` is an RWO PVC (and
  `RollingUpdate` for RWX or an ephemeral emptyDir).

### Fixed

- **A control-plane restart no longer fails healthy in-flight tasks (#858).**
  The agent-lost reaper failed any task whose last heartbeat was older than the
  threshold — but a control-plane restart (rollout, node drain, kubelet kill)
  makes *every* in-flight task look stale, because the process that records
  heartbeats is the one that was down. The new leader would then mass-reap
  healthy, still-running tasks. The reaper now honors a post-leadership **grace
  window** (2× the agent-lost threshold, 180s) measured from when this instance
  acquired leadership, so the fleet has time to re-heartbeat before any reap; a
  genuinely lost agent is still reaped once the grace elapses.
- **Infra re-placement is now backed off and jittered to avoid a thundering herd
  (#859).** When a task fails for an infrastructure reason (agent/pod/dispatch
  lost) it re-places without consuming its retry budget — but it did so
  immediately, so a mass infra fault (a restart marking a whole run agent_lost at
  once) re-dispatched every sibling on the same tick, stampeding the just-
  recovered kube-apiserver and re-throttling the scheduler. Re-placement now
  waits out an exponential backoff from the failure time (the same curve as a
  synchronous dispatch failure, keyed on the infra-attempt count) plus a
  deterministic per-task jitter, so siblings spread across a window instead of
  firing together.
- **A task killed by the agent-lost reaper now ends its log with a marker
  (#861).** When the control plane fails a task whose agent went silent, it
  deletes the pod and the log stream stops — previously leaving the task log
  truncated mid-line (e.g. `Running with dbt…`), indistinguishable from a hang.
  The reaper now appends a final `killed: agent_lost (last heartbeat …)` line to
  the reaped attempt's log before tearing the pod down, so the reason is visible
  where the operator tails it. Best-effort: the marker never blocks the reap.

## [0.4.3] - 2026-09-01

### Added

- **Task pods default to the operator's task ServiceAccount (#844).** A new
  `executor.task_service_account` (Helm: `taskServiceAccount`) is applied to every
  task pod — and to warm-pool pods — when a DAG does not pin its own, so cloud
  workload identity (EKS IRSA, GKE Workload Identity, AKS) works without wiring a
  ServiceAccount into each task. A task that pins a different SA still wins and is
  never placed on an incompatible warm pod (#853).
- **`leoflow dags list` (#842).** Lists the registered DAGs from the CLI, matching
  the operability of `leoflow runs`.
- **`leoflow login --password-stdin` (#838).** Reads the password from stdin so it
  never lands in shell history or a process listing, mirroring `docker login`.

### Changed

- **The in-pod agent baked into the runtime base image is now version-stamped.**
  `leoflow-agent version` in a task pod reports the version, commit, and build date
  it was built from (the same `-ldflags -X` as the release binaries), instead of an
  empty/`none` build. This restores commit-level traceability for the task pod and
  makes a control-plane↔agent version skew diagnosable. The runtime-image release
  job already publishes an immutable per-release tag (`py<ver>-<release>`) alongside
  the moving `py<ver>` line.
- **Airflow Task SDK bumped to 3.3.x (`apache-airflow-task-sdk==1.3.1`).** DAGs
  now run under the Airflow 3.3 Task SDK in the task pod and the `leoflow lite`
  dev venv; the 3.3 SDK surface is additive over 3.2 (no removed `airflow.sdk`
  exports), and the airflow-free parser shim is unaffected. The parser's optional
  real-Airflow backend accepts `apache-airflow>=3.2,<3.4`. The embedded web UI
  stays on the Airflow 3.2.x SPA and `/api/v2/` remains 3.2.x-compatible — the SPA
  upgrade is tracked separately. Validated on the pod-path e2e against a 3.3.1
  runtime image.
- **`leoflow compile` pins the immutable per-release task base image by default
  (#851).** A build from a released CLI now `FROM`s the per-release base tag
  (`py<ver>-v<X.Y.Z>`) instead of the moving `py<ver>` line, so a DAG image built
  today rebuilds byte-for-byte later; a dev/dirty CLI still tracks the moving tag,
  and an explicit `base_image` in `leoflow.yaml` always wins.

### Fixed

- **A database outage during login returned 401 "invalid credentials" instead of a
  5xx (#843).** `IssueToken` collapsed every credential-store error into
  `ErrInvalidCredentials`, so an unreachable DB read as "wrong password" — sending
  operators to chase a password problem during an outage and masking the incident.
  A genuine not-found now stays `ErrInvalidCredentials` (401, no user enumeration);
  any other backend error propagates and the login endpoint answers **503
  "authentication temporarily unavailable"** without consuming the per-IP lockout
  budget. The 503 leaks nothing (returned regardless of whether the account exists).

- **dbt managed connection now works under enforce scoping and an external
  secrets backend.** The dbt compiler wraps each task with the profile step that
  reads `AIRFLOW_CONN_<conn>`, but did not **declare** the managed connection — so
  the agent injected it only under permissive scoping (the whole vault). Under
  enforce scoping or the external secrets resolver (which deliver declared names
  only) the connection was missing and the task failed "not delivered". The
  compiler now declares it on every dbt task, across all warehouse adapters
  (postgres, snowflake, bigquery, databricks, duckdb). **Behavior change:** a dbt
  DAG with `connection:` now validates that connection at registration (it must
  exist, or be covered by an external backend) — create the connection before
  deploying, the same order any declared connection follows.
- **External secrets resolver failed against a real provider backend (ADR 0060).**
  Importing and initialising an Airflow provider secrets backend emits log lines
  on stdout (structlog, alembic, the secrets masker). The resolver's stdout is the
  strict-JSON channel the in-pod agent parses, so that logging corrupted the
  result and the agent rejected it as malformed, failing the task closed. The
  resolver now isolates its stdout: provider logging is redirected to stderr for
  the duration of the resolve and only the JSON result reaches stdout. The backend
  ships off by default (added in 0.4.2), so no released configuration was affected.
  Locked by a subprocess regression test and a new pod-path end-to-end test that
  resolves a declared connection and variable from an emulated Secrets Manager
  (LocalStack) in a real task pod.
- **A per-task `connections:` override dropped a dbt task's managed connection
  (#848).** Overriding a dbt task's `connections:` in `leoflow.yaml` replaced the
  compiler-declared profile connection, so the profile step failed "not delivered".
  The override now keeps the managed connection and adds the extra ones.
- **dbt task failures were hard to diagnose (#838).** A failing dbt invocation now
  surfaces its stderr in the task log instead of a bare non-zero exit, so a
  compile/model error is visible from the run.
- **A synthesized dbt image could not find `dbt` and left the project writable
  (#852).** The generated Dockerfile installed dependencies as the base's non-root
  user, so dbt's console script landed off `PATH` (`dbt: command not found`) and an
  apt step failed. Dependencies now install as root (scripts on `PATH`), the task
  drops back to the non-root UID before the source `COPY` (the project stays
  read-only), and dbt writes `profiles.yml`, `target/`, and logs to `/tmp` — the
  base image points `DBT_PROFILES_DIR`/`DBT_TARGET_PATH`/`DBT_LOG_PATH` there, and
  `profiles.yml` is written `0600`.
- **A missing declared secret under an external backend was hard to diagnose
  (#842).** When the resolver is configured, an unresolved declared Connection or
  Variable now logs the specific declared names that came back empty (scoped to the
  backend's coverage), pointing at the pod identity / backend permissions instead
  of failing silently. A non-email login username also gets a one-line hint (the
  login is the email address).

### Docs

- **Cluster-validation runbook for the native secrets resolver (#841)** — EKS/GKE
  keyless (IRSA / Pod Identity / Workload Identity) validation steps.
- **The task ServiceAccount auto-default applies only with
  `taskServiceAccount.create=true` (#849)**, with a bring-your-own-SA note in the
  chart README.

## [0.4.2] - 2026-08-31

### Added

- **Typed trigger form in the UI (#798).** The "Trigger DAG w/ config" dialog
  renders a generated form from a DAG's declared `params=`, with the raw JSON
  editor still one toggle away.
- **External secrets resolver (#811, ADR 0060) — off by default.** A declared
  Connection/Variable can be resolved pod-side from a provider store (AWS Secrets
  Manager reference; GCP / Azure / Vault via config) under the pod's own keyless
  identity, instead of the Leoflow vault. Operator-configured (`secrets.backend`
  + Helm); declaration-scoped, fail-closed, no copy at rest. Keyless end-to-end is
  validated on a real cluster (EKS/GKE RC) before enabling; the vault stays the
  only source when unset.

### Changed

- **`deferrable=True` rejected at compile time (#794, ADR 0016).** A clear error
  points to `deferrable=False` / `mode="reschedule"` instead of compiling a task
  that cannot defer at runtime.
- **OpenTelemetry tracing is off by default** — opt in via `observability.otel`.

### Security

- **OIDC break-glass is SSO-only (#827).** Under `provider: oidc`, an empty
  break-glass allowlist now denies all password logins instead of leaving
  `POST /auth/token` open.
- **Author task env can no longer override reserved `LEOFLOW_` variables
  (GHSA-3r74-9w27-v32f).** Closes an in-pod agent control-plane / credential
  redirect via a DAG's `env:`.
- **OIDC hardening (#827):** dotted tenant/group config keys, returning-user
  tenant-mismatch reject, rate-limited login/callback, and fail-closed handling of
  Entra group-claim overage.

### Fixed

- **Flaky scheduler alert-dedup test (#609).**

### Docs

- **Versioned documentation site with per-release archives (#814).**
- **External secrets how-to** (`operate/external-secrets.md`).

## [0.4.1] - 2026-08-28

### Added

- **`leoflow runs logs <dag> <run> <task> [--try N] [-f]` (#768).** Read a task
  attempt's logs from the CLI — it streams the existing task-instance logs endpoint
  (the same logs the UI shows), so a failed task's output is reachable without the
  UI. `--try` defaults to the latest attempt; `-f` follows a running task.
- **Airflow-3 alias imports accepted (#786).** `from airflow.decorators import task`
  and `from airflow import DAG` now resolve to the Task SDK (`airflow.sdk`), so a
  canonical Airflow-3 DAG compiles as-is. Legacy `airflow.operators.*` imports get a
  clear error naming the `airflow.providers.standard.operators.*` replacement (they
  were removed from core in Airflow 3.0), rather than a mistranslation.
- **Trigger-time DAG parameters (#545).** `leoflow runs trigger --conf '{…}'` /
  `--conf-file` supplies a run's `conf`, which reaches tasks as `{{ params.x }}`;
  declared `params=` (Airflow's typed `Param`) are captured, defaulted, and validated
  at trigger time (a schema violation is a 400), and a `Param` with no default is
  required (a trigger that omits it is a 400).
- **DAG scheduling/metadata attributes honored (#797).** `max_active_runs`, `catchup`,
  `start_date`, and `description` set on the `DAG(...)` are now captured and honored
  instead of silently dropped.
- **How-to: trigger a run from an external system (#799).** A copy-paste REST flow
  (obtain a JWT → `POST …/dagRuns` with `conf` → poll the run) for an external
  integrator.

### Fixed

- **`leoflow deploy --build` now works for a pure-dbt DAG (#769).** The synthesized
  Dockerfile assumed a `dag.py` and failed the build (`COPY dag.py`) for a dbt-only
  project; it now copies the dbt project (and its baked manifest) instead. A DAG
  shipping its own Dockerfile is unaffected.
- **A task refused for running as root now says so (#766).** A `CreateContainerConfigError`
  from the non-root admission (`runAsNonRoot`) previously left the task polling and
  its `failure_reason` generic; it now settles `failed` and names the cause + the
  fix (end the image with a numeric `USER 65532`, or set `taskPodSecurity.runAsNonRoot=false`).
- **Warm pools no longer place a staging DAG on a warm worker (#788, ADR 0058 D5).**
  A staging attempt could be assigned to a reused warm pod that carries no per-run
  `/staging` mount, silently diverging the run's shared-volume data; staging versions
  now always take a dedicated pod and are excluded from the warm reconciler. Only
  reachable with warm pools enabled (`min_idle > 0`), off by default.
- **`@task.branch` fails with a clear error, not an opaque `AttributeError` (#809).**
  The TaskFlow branch spelling now reports that branching is not supported yet
  (ADR 0040 Phase D), matching how `BranchPythonOperator` is already refused, instead
  of a confusing import-time crash.
- **Warm-pool docs lead with the safe default and the dormant `min_idle_workers`
  seam is documented honestly (#789).** The enable guide now leads with
  `minIdleWorkers: 0` (scale-to-zero) and the not-yet-author-settable field is marked
  dormant rather than reading as usable.
- **Docs corrections (#790, #795).** Fixed broken cross-references/anchors, and the
  stale claim that reschedule-mode sensors were unsupported (they are); deferrable
  operators are recorded as a conscious non-goal (ADR 0016) with the reschedule
  alternative.

### Security

- **The `leoflow-server` control-plane image is now cosign-signed (#772),** closing
  the one published image that was unsigned (migrate + runtime already were). A
  release-gate step verifies the server signature and drafts the release if it is
  missing.
- **Release image signing retries transient OIDC flakes (#773).** Every `cosign sign`
  now retries with backoff, so a momentary Sigstore/OIDC blip no longer drafts an
  otherwise-good release.

### Docs

- **External secrets how-to (#812).** Reaching a credential that lives in AWS
  Secrets Manager / GCP Secret Manager / Azure Key Vault / HashiCorp Vault from a
  task pod — keyless (Workload Identity) first, then an ESO/CSI-synced or mounted
  Kubernetes Secret — without duplicating it in Leoflow, plus how a secret is
  scoped to the right pod.
- **The pod model — "when you pay for a pod" (#785).** Pod-per-task and the levers
  that avoid its cold-start cost (the subprocess dev executor, `dbt_group`, warm
  pools); records the generic fused-`TaskGroup` as a deliberate, deferred non-goal.
- **Deferrable operators recorded as a conscious non-goal (ADR 0016, #795),** with
  reschedule-mode sensors documented as the supported alternative.

## [0.4.0] - 2026-08-26

> Leoflow's biggest release to date. The headline work: **warm worker pools**
> (N:1 pod reuse across attempts, ADR 0058), first-class support for a
> **shared, multi-team Kubernetes cluster** (named task pools, per-DAG
> admission, a shared-informer read path, full placement/DRA passthrough, and
> quota/APF backpressure that no longer burns a task's retry budget — ADR
> 0053/0054), **RBAC + OIDC/SSO** with fail-closed tenant pinning, **per-task
> secret scoping and short-lived agent credentials** (the mechanism ADR 0045
> left unshipped, ADR 0055), a **durable task-outcome** model that stops
> pod-death-mid-report from reading as a false failure (ADR 0051/0052), and a
> native **S3/GCS object log sink** (ADR 0056). Below that sits the rc.1→rc.4
> hardening batch (Helm-reachability fixes, diagnostics, CLI polish).

### Added

#### Warm worker pools — N:1 pod reuse across attempts (ADR 0058)

- **A task pod can now be reused across attempts instead of spawned fresh for
  every one.** A warm worker's agent runs in a long-lived fork-per-attempt
  mode (#676) over a dedicated gRPC assignment transport with ack/lease
  semantics (#673), wired into scheduler dispatch (#675). Off by default —
  `execution.warm_pools_enabled` defaults to `false`, a byte-for-byte no-op —
  and the control plane **fails closed at boot** if it is turned on without
  the ADR 0058 D2 security prerequisites already in force: the projected-SA
  token-exchange transport and enforce-mode secret liveness (#671).
- **Pool lifecycle.** A min-idle reconciler provisions warm pods and never
  deletes a busy worker (#677, busy-aware fix #681, terminal-pod-skip fix
  #678); each attempt gets a durable attempt→worker binding persisted on ack
  (#679), backed by a failover reaper and double-run guard so two agents can
  never claim the same binding (#680); an idle-slot reclaim path unstrands a
  worker and fast-re-places its next attempt (#682); and every warm worker
  enforces its own lifecycle — max attempts, max lifetime, an idle TTL, a
  drain path, and an attempt watchdog (#683).
- **Guardrails.** A per-tenant aggregate cap on warm pods, reserved and
  rationed rather than first-come (#686); a per-DAG-version ConfigMap
  GC-anchor so a warm pod can't outlive the DAG version it was spawned for
  (#687); and a worker-scoped bootstrap token exchange so a warm pod never
  holds a long-lived plaintext credential across the attempts it serves
  (#689).
- **Reachable from the Helm chart, with the same fail-closed guard the server
  enforces (#695)** — see the full entry below.

#### Shared-cluster / multi-team Kubernetes (ADR 0053, ADR 0054)

- **Leoflow can now share a cluster without starving its neighbors or letting
  them starve it.** A per-DAG `max_active_tasks` admission gate caps how many
  of one DAG's tasks may be queued/running at once (#632), and named,
  cross-DAG task pools (Pro-only) add a second, budget-based admission gate on
  top of it (#642) — replacing the long-standing `/api/v2/pools` stub. A
  shared pod informer replaces the reapers' per-second `LIST` storm and the
  reconciler's 30-second `LIST` with one long-lived watch (#634).
- **Placement and scheduling passthrough.** Declared tolerations (#621) and
  the rest of the placement surface — `priorityClassName`,
  `terminationGracePeriodSeconds`, `runtimeClassName`, topology-spread
  constraints, affinity, DRA resource claims, ephemeral-storage, and custom
  labels/annotations (#630) — now actually reach the pod spec instead of
  being accepted at push and silently dropped.
- **Quota/APF backpressure is no longer billed to the task's retry budget.** A
  `ResourceQuota` 403 or an API Priority & Fairness 429 on pod create now
  retries forever without incrementing `dispatch_attempts`, so a fan-out under
  a tight quota self-throttles instead of producing a wave of false
  `dispatch_failed` terminal states (#631).

#### Identity & access

- **RBAC foundation.** A seeded viewer/editor/operator role ladder, with roles
  and permissions reloaded from the store on every token verify — so a
  deactivated or deleted user loses access immediately, not at token TTL —
  and nav menus filtered to what the caller can actually do (#654).
- **OIDC/SSO with fail-closed tenant pinning (ADR 0057), Pro-gated.**
  Authorization Code + PKCE against an external IdP (Entra ID / Google Cloud
  Identity / Okta and any standard OIDC provider), JIT-provisioned users, and
  every login reconciling the user's roles to the IdP-mapped set — a pin
  failure is a 403, never a default-identity fallback (#656).
- **User management.** `POST /api/v2/users` to create an account and
  `GET /api/v2/users` to list them, admin-gated (#627, #649); `leoflow admin
  users list` (#651); and a new `leoflow admin` operator CLI —
  `health`/`dags pause`/`drain`/`runs list` — for operating a running Pro
  control plane over the typed client (#635).

#### Secret scoping & agent credentials (ADR 0055 — the ADR 0045 follow-up)

- **The mechanism to stop every task from receiving the whole tenant vault.**
  ADR 0045 was accepted but never shipped; this is that code. A declared-secret
  schema lets a DAG or task name the variables/connections it actually needs
  (#660), narrowing is enforced by a scope-by-policy gate paired with a
  task-liveness check that stops a superseded or finished attempt's still-valid
  token from resolving secrets (#663), and a task that is narrowly declared but
  would still receive the full vault now gets a warning (#661). **Ships in
  observe/permissive mode by default — nothing is denied yet** until an
  operator flips it to enforce. Agent credentials also got shorter-lived:
  a per-attempt token is now renewed on every heartbeat instead of living for
  the whole attempt (#665), and a projected-ServiceAccount token-exchange
  transport replaces the plaintext-env-var credential, flag-gated off (#667).

#### Durable execution (ADR 0051, ADR 0052)

- **A task's true outcome is no longer conflated with whether its report
  survived.** If a pod dies mid-report (OOM, eviction) after the task itself
  succeeded, the reconciler can now recover that success from a durable
  outcome record instead of settling it as a failure (#615). Infra faults —
  a lost agent, pod, or dispatch — now re-place the task without consuming
  its own retry budget, bounded instead by a separate infra-attempt limit, so
  an exhausted infra budget with retries remaining still finalizes rather
  than hanging (#614). The four backstop reapers and the pod lifecycle moved
  behind a dedicated execution seam ahead of warm pools, with no behavior
  change (#669, #670).

#### Object log sink (ADR 0056)

- **Task logs can now be written straight to S3 or GCS instead of an
  in-cluster PVC.** A native dual-SDK backend (`aws-sdk-go-v2` for S3/MinIO,
  the native `cloud.google.com/go/storage` client for GCS — not the S3
  interop endpoint, which cannot use Workload Identity) is keyless-first via
  IRSA or GKE Workload Identity. Off by default; the on-disk sink is
  unchanged for every deployment that does not opt in (#640).

#### Deploy & MCP

- **`helm install` no longer requires cert-manager just to turn on agent
  TLS.** The chart now auto-generates a stable self-signed CA and gRPC server
  cert by default (`agentTLS.autoGenerate`), reusing the same CA across `helm
  upgrade` so it never silently rotates and breaks running agents; bring-your-
  own/cert-manager remains the opt-in production path (#690, #692).
  - The Helm chart is now published as a signed OCI artifact on every release
    and RC tag (`oci://ghcr.io/neochaotic/charts/leoflow`) (#691).
- **MCP `search_logs` can search a whole run, not just one known task.**
  `task_id` is now optional — when omitted, the tool enumerates the run's
  task instances and searches each one, tagging every match with the
  `task_id`/`try_number` it came from (#612).

#### Diagnostics & CLI

- **Transparent CLI token renewal — no more hourly re-login (#755).** The CLI now
  silently refreshes a file-persisted session token once it passes the halfway
  point of its life (rewriting `~/.leoflow/config.yaml`), so long sessions no
  longer hit `401 missing bearer token` every hour. Backed by a new
  `POST /api/v2/auth/token/renew` that re-mints the same identity with a fresh
  short TTL, bounded by `auth.jwt.max_lifetime_seconds` (default 24h). The
  access-token TTL is unchanged and revocation is still enforced per request, so a
  renewed token dies the moment its user is deactivated.
- **`leoflow runs list` (#747).** The common `runs` verb now lists DAG runs
  (`--state`/`--dag`/`--older-than`) alongside `trigger` and `status`, reusing the
  same lister as `leoflow admin runs list`.
- **A failed task now says why, even when it produced no logs (#698).** When a
  task pod died before its agent ever registered with the control plane — bad
  RBAC, a TLS trust failure, a network policy, image-pull auth, or an OOM at
  startup — the API reported `"state": "failed"`, `"hostname": "unknown"` and no
  cause of any kind, and the UI showed `No logs available for this attempt.`
  (correct, since nothing ever streamed). Diagnosing it required `kubectl logs`
  against the cluster, so an operator without cluster access could not diagnose
  it at all. Three changes close that blind spot:
  - Task instances now expose a **`failure_reason`** field (a Leoflow extension;
    the `/api/v2/` Airflow surface is unchanged and purely additive). The cause
    was already recorded in `task_instances.error_message` by the reapers and the
    reconciler, but was dropped before it reached the domain model or the API.
  - The agent now classifies a failure it hits **before registering** and leaves
    it on the container termination message, which the reconciler already reads —
    so a refused bootstrap token exchange, an unreachable control plane, or an
    unreadable projected token becomes a reason on the attempt instead of a
    silently discarded server-side log line. The reason is drawn from a closed
    set of operator-facing classifications; a raw internal error or a credential
    is never echoed into this end-user-visible field.
  - Failures only Kubernetes can see are described precisely rather than as a
    bare `pod failed`: the task container's terminated reason and exit code
    (including `OOMKilled`), the pod-level reason (e.g. `Evicted`), and the
    unrecoverable waiting reasons (`ImagePullBackOff`, `CreateContainerError`, …)
    now carry the context that makes them actionable.

  The reason is also appended to the otherwise-empty log view as an error-level
  event, so the cause appears where an operator is already looking. It is
  best-effort and diagnostic only: it is null when nothing observed a cause, it
  is bounded in length, and it does not change retry accounting or any state
  transition.
- **Warm worker pools are reachable from the Helm chart (#695).** The server has
  supported them since ADR 0058, but the chart exposed none of the knobs and had
  no escape hatch, so the only way to enable the feature on a Helm-installed
  release was `kubectl set env` behind Helm's back — an operator following the
  docs could not reach it at all. New values: `execution.warmPoolsEnabled`,
  `execution.minIdleWorkers`, `execution.maxPoolSize`,
  `execution.maxAttemptsPerWorker`, `execution.maxWorkerLifetime`,
  `execution.workerIdleTtl`, `execution.maxWarmPodsPerTenant`,
  `auth.agentTokenTransport` and `auth.secretLivenessMode`, plus a general
  `extraEnv` list for `LEOFLOW_*` settings the chart does not model. Every
  default equals the server's, so an unchanged `helm upgrade` keeps today's
  behavior: warm pools off, `envvar` transport, liveness in `observe`. The chart
  now **refuses to render** when warm pools are enabled without their ADR 0058 D2
  security prerequisites (`agentTokenTransport=exchange` **and**
  `secretLivenessMode=enforce`), mirroring the server's fail-closed boot check —
  a clear render error instead of a `CrashLoopBackOff` explained only in
  container logs.

### Fixed

- **Duplicate `dag_version` push returns 409, not a raw 500 (#746).** Pushing a
  version that already exists with different content (the common
  `tag_strategy: version`→`dev` dev loop) collided on the unique constraint and
  surfaced `500 internal error` leaking the Postgres SQLSTATE/constraint name. It
  now returns **409 Conflict** with no internal details; an identical re-push is
  still an idempotent no-op.
- **`leoflow.yaml` `defaults.resources` and `defaults.node_selector` now apply to
  tasks.** Both were accepted by the schema but silently dropped, so DAG-wide
  resource/placement defaults never reached the pod (and a DAG relying on
  `defaults.resources` for Guaranteed QoS silently got BestEffort). They are now
  applied as a per-task fallback at compile time (a task's own settings still
  win), and unknown keys in the `defaults` block now fail loudly at compile
  instead of being ignored.
- **`read_only_task_root_filesystem` no longer prevents a pod from starting
  (#741).** The hardening flag left no writable `/tmp`, so a read-only-rootfs task
  — and especially a warm worker, which died before registering — could not run. A
  writable `emptyDir` is now mounted at `/tmp` on task and warm pods whenever the
  flag is set (default pods are unchanged), making the mitigation the warm-pool
  docs recommend actually usable.
- **Managed CPython is version-checked and never silently falls back to an
  unsupported interpreter (#742).** `leoflow` re-installs the managed interpreter
  when its pinned version changes (via a `.py-version` sentinel, like managed
  Postgres), prefers the checksum-verified build over a host `python3.11`, and
  rejects an unsupported interpreter with a "run `leoflow setup`" hint instead of
  proceeding on system `python3`. Archive extraction is hardened against
  interrupted and cross-version upgrades.
- **`executor.defaults.staging_size` and `executor.defaults.staging_storage_class`
  are now reachable in a Helm install (#743).** Like the earlier
  `trusted_proxies`/resource-defaults fix, these keys were absent from the config
  defaults so their `LEOFLOW_*` env vars never bound; they are now registered and
  exposed as chart values.
- **The chart version is gated against the release tag, and the chart is published
  to OCI (#748).** A pre-tag check fails the release when `Chart.yaml`
  `version`/`appVersion` lags the tag (which made a default `helm install` pull the
  previous release's images); the OCI chart publish
  (`oci://ghcr.io/neochaotic/charts/leoflow`) is now gated behind that check and
  documented.
- **Docs tooling cleaned up after the Hugo migration (#744).** The `ci-local`
  docs gate now runs `hugo --gc --minify` instead of the removed MkDocs build,
  `CONTRIBUTING` points at a template that exists again, and the CI path filters
  account for `website/`.
- **Secret-scope audit events are now recorded (#722).** `RecordSecretScopeWarning`
  and `RecordSecretLivenessDenial` resolved the tenant argument by name, but the
  agent RPC path calls them with the tenant **UUID** carried by the agent token —
  so every ADR 0055 audit row failed silently with `resource not found`, including
  the enforce-mode `secret.liveness_denied` security record. Both now resolve by
  id, so the observe-mode readiness trail and the enforce-mode denials are
  actually persisted rather than existing only as log lines.
- **A retried task instance no longer wedges in `queued`/`running` (#723).** The
  reaper liveness check (`TaskPodActive` and its cache fast-path `CachedPodActive`)
  matched a pod by `(run, task)` only, so a lingering earlier-attempt pod made the
  dispatch-lost and pod-lost reapers defer forever once a retry bumped
  `try_number`. Both checks now pin `leoflow.io/try-number`, asking liveness about
  the attempt being failed rather than any pod for the task.
- **`server.trusted_proxies` and `executor.defaults.resources_*` are now
  reachable from a Helm install (#725).** These keys were absent from
  `serverDefaults`, and viper's `AutomaticEnv` only binds an env var for a key it
  has seen via `SetDefault` — so `LEOFLOW_SERVER_TRUSTED_PROXIES` and
  `LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_CPU`/`_MEMORY` were silently dropped. The
  chart ships no server config file, so env is the only override path, which left
  two production defaults unconfigurable: with trusted proxies unset the login
  rate-limiter keyed every request on the ingress IP, so a handful of bad logins
  locked out the whole deployment; and with the resource defaults unset a task
  that relied on them ran BestEffort (first evicted under node pressure). The
  keys are now registered (`redis.ca_file` was missing from the same map and is
  fixed too), `trusted_proxies` binds from a comma-separated env var (viper
  splits it into a list), and the L0 resource default now sets **both** requests
  and limits so such tasks reach Guaranteed QoS. The chart exposes
  `config.trustedProxies` and `executor.defaults.resources`.
- **The api role no longer receives the gRPC TLS private key in split mode
  (#726).** The `grpc-tls` volume was gated only on `agentTLS.enabled`, so the
  internet-facing api Deployment mounted the whole cert Secret — including
  `tls.key` — although only the scheduler serves the agent gRPC listener. The
  volume and mount are now scoped to the scheduler role (the TLS env vars stay on
  both roles so the Pro boot guard still passes), keeping the private key off the
  api pod (ADR 0049).
- **Warm-pool attempts now get a fresh `TMPDIR` (#728).** A warm worker reused one
  pod across attempts but never redirected `TMPDIR`, so anything a task wrote under
  `/tmp` (a dbt profile, a cached credential) persisted to the next attempt on the
  same worker — a filesystem channel around the per-task secret scoping. Each
  attempt now gets a `TMPDIR` inside the per-attempt scratch that is wiped between
  runs; the warm-pools doc is corrected to state the actual isolation guarantee
  (image-level paths and `$HOME` still persist — use `read_only_task_root_filesystem`).
- **Repository validation errors now return 400, not 500 (#724).** A DAG version
  declaring an unknown variable or connection produced `500 internal error`
  (reading as a server fault) because `handleRepoError` had no
  `domain.ErrValidation` branch. It now maps validation errors to `400 invalid
  request`, so the actionable message points the user at `leoflow connections set`
  instead of the server logs.
- **The migration Job no longer mounts an unused ServiceAccount token (#727).** The
  Job talks only to Postgres but received the namespace default SA's projected
  token; it now sets `automountServiceAccountToken: false`, matching the task-pod
  path.
- **`leoflow lite --postgres managed` is now idempotent across upgrades (#729).**
  Re-extracting the managed Postgres bundle failed with `file exists` because
  symlink extraction was not idempotent; it now removes an existing target before
  creating the link. The managed-Postgres guard is also version-aware (keyed on the
  bundled Postgres version), so a newer bundle is re-installed on upgrade instead
  of silently keeping the old one.
- **Chart RBAC now grants the `tokenreviews` permission the token-exchange
  transport requires (#696).** With `auth.agentTokenTransport=exchange` the
  control plane validates a task pod's projected ServiceAccount token via the
  Kubernetes TokenReview API, but the chart granted only a namespaced Role —
  and TokenReview is cluster-scoped, so no Role could ever carry it. The review
  was refused (`tokenreviews.authentication.k8s.io is forbidden ... at the
  cluster scope`) and every task pod died on `projected token is not valid`.
  Because the transport is server-wide, this broke the ordinary dedicated
  pod-per-task path too, not only warm pools — which made it a hard blocker for
  warm pools, whose D2 prerequisites force the transport on. The chart now
  renders a ClusterRole + ClusterRoleBinding granting `create` on
  `authentication.k8s.io/tokenreviews`, named `<fullname>-<namespace>-tokenreview`
  so multiple releases in one cluster cannot collide, bound to the same
  ServiceAccount as the executor Role (the scheduler SA in split mode). It is
  rendered **only** on the `exchange` transport: `create` on `tokenreviews` is an
  authentication oracle, and an install on the default transport never issues the
  call. `scripts/rbac-covers-executor.sh` now checks this grant against the code
  the same way it checks the executor's, so the two cannot drift apart again.
- **`leoflow runs trigger`, `leoflow runs status` and `leoflow dags delete` now
  use the token saved by `leoflow auth login`.** These commands read the server
  URL from `~/.leoflow/config.yaml` but resolved the bearer token only from an
  explicit `--token` or `LEOFLOW_TOKEN`, so a user who had just logged in
  successfully got `401 missing bearer token`. They now share the same
  `--token` → `LEOFLOW_TOKEN` → config-file precedence that `push`, `deploy` and
  the `admin` subcommands already applied. (#697)
- **The control plane now fails closed at boot when the `exchange` agent-token
  transport has no Kubernetes client (#700).** It previously logged a warning and
  left the exchange unwired, so with `auth.agent_token_transport=exchange` every
  task pod's bootstrap failed `Unimplemented` while `/readyz` stayed green. It now
  refuses to start with a clear error naming the missing in-cluster/kubeconfig
  client, so the misconfiguration is visible at boot instead of silently failing
  every task.
- **`leoflow dev` no longer panics on a nil context while probing the companion
  binary version (#699).** The probe received `cmd.Context()`, which is nil when
  the command was not run through cobra's `Execute()`, and `context.WithTimeout`
  panicked `cannot create context from nil parent`. It now routes through the
  package's nil-guarding context helper and falls back to a background context —
  a best-effort probe must never crash the CLI.

### Changed

- **Task pods now run as non-root by default (behavior change; breaking for root
  task images).** The default for `taskPodSecurity.runAsNonRoot`
  (`executor.defaults.run_tasks_as_non_root`) flips `false → true`, and the task
  base image's UID is renumbered `1000 → 65532` (distroless `nonroot`, matching
  the control-plane and migration pods) — this unblocks Pod Security Admission
  `restricted`. On upgrade, any existing task image that runs as root will fail to
  start with `CreateContainerConfigError` ("container has runAsNonRoot and image
  will run as root"), because the pod sets `runAsNonRoot=true` without pinning
  `runAsUser`. **Remediation for a fleet with root task images:** set
  `taskPodSecurity.runAsNonRoot=false` (cluster-wide — pod security is
  intentionally not a per-DAG setting), or rebuild those images to run as a
  non-root user. The staging PVC stays writable for any non-root UID via the
  pod's supplementary `fsGroup`.

## [0.3.0] - 2026-08-13

> Promotes the **0.3.0** release line (`v0.3.0-rc.1` → `v0.3.0-rc.4`) to stable.
> The candidate was validated end-to-end on the published artifact: checksum +
> cosign signature verification, a binary-only install (managed Postgres, no repo
> or `PYTHONPATH`), the full UI journey (trigger, failure-path traceback,
> connection CRUD with encryption verified at the database), the `leoflow-mcp`
> server over stdio, and a dbt-duckdb run. See the `0.3.0-rc.1` … `0.3.0-rc.4`
> sections below for the full change list. The only change since `rc.4` is the
> front-door documentation fix below.

### Documentation

- **Front-door docs fixed for the v0.3.0 line.** The README's flagship example
  imported `from leoflow import DAG, task` (which does not parse — the shim only
  exports `dbt_group`); it now uses `from airflow.sdk import DAG, task`. Broken
  README links to `docs/api-reference.md` and `docs/helm-chart.md` now point to
  the published API reference and `helm/leoflow/README.md`. Install commands no
  longer pin the removed `v0.0.1*`/pre-alpha tags. Pro's readiness is now stated
  consistently across the README, index, quickstart, editions, and operating-modes
  pages ("Helm-installable and in active validation"), and the Status section
  reflects v0.3.0 (dbt, `leoflow-mcp`, `pkg/client`). (#601, #602, #603)

## [0.3.0-rc.4] - 2026-08-13

> Fourth RC of the **0.3.0** line — a one-line CLI consistency fix over `rc.3`:
> `leoflow --version` (flag) now works, matching the `version` subcommand and the
> companion binaries. No control-plane, MCP, dbt, or UI change since `rc.3`.

### Fixed

- **`leoflow --version` (flag) now works, matching the `version` subcommand and
  the companion binaries.** Previously the root CLI accepted only `leoflow
  version`; `leoflow --version` errored with `unknown flag`. The flag now prints
  the same build info and exits 0. (#598)

## [0.3.0-rc.3] - 2026-08-13

> Third RC of the **0.3.0** line — operability polish surfaced by the `rc.2`
> soak. The companion binaries (`leoflow-server`/`-agent`/`-mcp`) now answer
> `--version` and `--help` without a runnable config, and the embedded UI's
> documentation links resolve (the Airflow VersionInfo endpoint reports the
> pinned Airflow UI version, not leoflow's build version). No control-plane,
> MCP, or dbt functional change since `rc.2`. Docker-free gates green locally;
> full CI battery gated the cut.

### Fixed

- **`leoflow-server`, `leoflow-agent`, and `leoflow-mcp` now answer `--version`
  and `--help`.** Previously only the main `leoflow` CLI could report its
  version, and the companion binaries responded to `--version`/`--help` by
  trying to boot and erroring on missing config or an unreachable control plane.
  They now print their build version (`--version`) or a usage message
  (`--help`) and exit 0 before any config load or network connect, so an
  operator can identify and inspect a deployed binary. (#593)
- **The UI's "Learn more" documentation links no longer 404.** `GET
  /api/v2/version` (the Airflow VersionInfo endpoint the embedded SPA reads to
  build `https://airflow.apache.org/docs/apache-airflow/<version>/…` links)
  reported leoflow's build version, so the SPA pointed at a nonexistent Airflow
  docs release. It now reports the pinned Airflow UI version. leoflow's own
  version remains on the CLI, health endpoints, and the MCP
  `health://control-plane` resource. (#594)

## [0.3.0-rc.2] - 2026-08-13

> Second RC of the **0.3.0** line — a fix-and-polish pass over `rc.1`. Repairs two
> distribution bugs the `rc.1` soak surfaced (the **`leoflow-mcp`** binary is now
> built and shipped in the release archives; a fresh **binary-only install** —
> `leoflow compile` and `leoflow lite` — works without a repo or `PYTHONPATH`), and
> closes the discoverability gap on the new dbt cloud auth: the **connection form
> now surfaces** the Snowflake key-pair, BigQuery keyless, and Databricks OAuth M2M
> fields, with matching docs. No functional change to the MCP server or the
> control plane since `rc.1`. Docker-free gates green locally; full CI battery
> gated the cut.

### Added

- **The connection form now surfaces dbt cloud-auth fields with inline help.**
  The three modern service-account auth modes shipped in 0.3.0-rc.1 were only
  reachable by hand-typing keys into the raw `extra` JSON box, because the form
  catalog is generated from Airflow's provider introspection, which does not
  describe them. A hand-curated leoflow overlay
  (`internal/connectors/catalog.overlay.json`, merged over the generated catalog
  at load) adds them as labeled fields: Snowflake `private_key_passphrase`,
  BigQuery's keyless `method` selector, and the full Databricks form
  (`http_path`, `client_id`/`client_secret`, `auth_type`, plus a labeled
  workspace URL and access-token field — Airflow shipped it empty). The generated
  `catalog.json` is untouched; the overlay survives regeneration.

### Documentation

- **dbt: the warehouse-connection section now documents modern cloud auth.**
  `docs/dbt.md` §4 described only the legacy password/key-file modes; it now has
  a per-warehouse table (Snowflake key-pair, BigQuery keyless, Databricks OAuth
  M2M) and links to the connection reference pages that carry the full setup.

### Fixed

- **Fresh binary-only install: `leoflow compile` and `leoflow lite` now work
  without a repo or `PYTHONPATH`.** The primary "download the release and run
  it" path was broken: the binary extracts the Python parser to
  `~/.leoflow/pysrc/parser`, but spawned it as a bare `python3 -m leoflow_parser`
  without putting that directory on `PYTHONPATH`, so compile failed with
  `No module named leoflow_parser`. And the Lite boot referenced the extracted
  `pysrc/runtime/python` (to build the per-DAG venv) *before* anything extracted
  it, aborting with `does not exist`. The parser invocation now always wires the
  extracted sources onto `PYTHONPATH`, and the Lite boot self-heals the
  extraction before provisioning the venv. Every existing e2e job preset
  `PYTHONPATH=parser` (repo-relative), which masked both — a new
  binary-only-install CI gate exercises the genuine path (built binary, clean
  HOME, no repo, no `PYTHONPATH`). (#587)
- **The `leoflow-mcp` binary is now built and shipped in the release archives.**
  GoReleaser built `leoflow`/`leoflow-server`/`leoflow-agent` but not
  `leoflow-mcp`, so v0.3.0-rc.1 shipped the MCP server as source with no
  distributable binary. It now builds for the same platform matrix, carries its
  version via the `main.version` ldflag, and is included in the per-platform
  archive. (#586)

## [0.3.0-rc.1] - 2026-08-12

> First RC of the **0.3.0** line. Adds the experimental **`leoflow-mcp`** Model
> Context Protocol server (read tools + resources over stdio and Streamable HTTP,
> ADR 0050) and a typed **`pkg/client`** for `/api/v2`; brings **dbt** cloud-adapter
> auth up to modern service-account standards (Databricks OAuth M2M, Snowflake
> key-pair, BigQuery keyless), a dbt-aware `diagnose_run`, and adapter contract
> tests; plus security hardening (seccomp, trusted-proxy default, `/metrics`
> isolation). Docker-free gates green locally; full CI battery green on the tagged
> commit (`f3ded10`).

### Added

- **`diagnose_run` (MCP) is now dbt-aware and shows downstream impact.** For each
  failed task it additionally surfaces the tasks it transitively **blocks**
  (`downstream_blocked`, from the DAG's `depends_on` graph) and, for a dbt task,
  the **models** it runs (`models`, parsed from the task's `--select` — a single
  model at `node` granularity, several for a fused group). The summary now
  distinguishes root failures from the tasks they blocked. Best-effort from the
  compiled spec — a run with no failures pays nothing extra, and a spec that can't
  be read just omits the fields. No control-plane change (built on the existing
  `dag://spec`).
- **Databricks dbt profiles support OAuth M2M (service principal).** A managed
  Databricks connection whose extra carries `client_id`/`client_secret` (or an
  explicit `auth_type: oauth`) now generates a `profiles.yml` using service-principal
  OAuth M2M — Databricks' recommended auth for automation — instead of a Personal
  Access Token. PATs keep working; OAuth wins when present (the two are mutually
  exclusive). Same encrypted-at-rest, generated-in-pod delivery as before.
- **Snowflake dbt profiles support key-pair auth (service account).** A managed
  Snowflake connection whose extra carries `private_key_content` (inline PEM) or
  `private_key_file` (path) — with an optional `private_key_passphrase` — now
  generates a `profiles.yml` using key-pair auth instead of a password. Passwords
  keep working; key-pair wins when a key is present (password/token dropped).
  Prefer it for automation — Snowflake is deprecating single-factor password auth.
- **BigQuery dbt profiles support keyless auth (Workload Identity).** Setting
  `method: oauth` in a `google_cloud_platform` connection's extra now generates a
  BigQuery `profiles.yml` that authenticates via Application Default Credentials
  (GKE Workload Identity on Pro) — no service-account key file is shipped.
  `keyfile_dict` (inline key) still works for the service-account-json method.

### Testing

- **dbt adapter contract tests (Snowflake / BigQuery / Databricks).** CI now feeds
  the `profiles.yml` Leoflow emits for each cloud warehouse through that warehouse's
  *real* dbt adapter credential parsing — validating field names, alias resolution,
  required fields, and each auth mode (key-pair / keyless / OAuth M2M) without a live
  query or credentials (`dbt-adapter-contracts` matrix). This is the credential-free
  half of cloud-adapter assurance; a live-query gate remains maintainer-owned (needs
  warehouse secrets). See the new "Adapter assurance" section in docs/dbt.md.

### Documentation

- **dbt: fused-group retry trade-off** — documented that retrying a `level`/`folder`
  (fused) group re-runs the whole group, with guidance to use `granularity: node`
  where per-model retry efficiency matters.
- **dbt: baked manifest & Slim CI** — documented where the compiled `manifest.json`
  lives and how it enables `state:modified+` Slim CI, plus an honest note on the two
  capabilities still needed for a turnkey recipe.

- **Experimental MCP server skeleton (`leoflow-mcp`)** — the first slice of the
  Model Context Protocol server (ADR 0050): a read-only, stdio server built on
  the official `modelcontextprotocol/go-sdk`, exposing `list_dags`, `diagnose_run`, and `search_logs` (one call: run state + failed tasks +
  each failed task's truncated, sanitized log tail) — reaching the control plane
  only through `pkg/client` (the caller's token is
  passed through; the server holds no privilege of its own). Optional and never
  part of `leoflow-server`. It also serves addressable read resources —
  `dag://list`, `run://detail/{dag_id}/{run_id}`, `task://instances/{dag_id}/{run_id}`,
  `log://task/{dag_id}/{run_id}/{task_id}/{try_number}` (truncated + sanitized),
  `dag://source/{dag_id}` (the dag.py text), `dag://spec/{dag_id}` (the compiled
  dag.json artifact, via a new GET /api/v2/dags/{dag_id}/spec endpoint), and
  `health://control-plane` (component health + executor + version). Beyond stdio
  it also speaks **Streamable HTTP** (`--transport http`, `POST /mcp`, stateless)
  as an optional Pro service, where each request carries its own bearer and the
  server mints a per-request control-plane client from it — the token
  pass-through the Pro deployment relies on, with no ambient privilege. Authoring
  follows.
- **A generated, typed Go client for the `/api/v2` surface (`pkg/client`)** — the
  single control-plane client the CLI, the coming MCP server, and smoke tests
  share instead of hand-rolling HTTP (ADR 0050). Generated from the OpenAPI spec
  with `oapi-codegen` (`make pkg-client`); CI fails on drift. The public OpenAPI
  spec now carries an `operationId` on every operation, so any consumer's code
  generator produces stable, idiomatic method names (`ListDagRuns`, `GetTaskLogs`,
  …). `leoflow auth create-token` / `login` now go through this client.

### Security

- **Control-plane and migration-Job pods now set `seccompProfile: RuntimeDefault`**
  (audit follow-up). The chart hardened these pods with `runAsNonRoot`, dropped
  capabilities, and `allowPrivilegeEscalation: false`, and a comment claimed a
  RuntimeDefault seccomp profile — but it was never rendered, so a cluster
  enforcing the `restricted` Pod Security Standard would reject the pods. The
  profile is now set at the pod level (inherited by every container) on the api,
  scheduler, monolith, and migration-Job pods. Task pods already carried it.
- **The API now trusts no proxy by default** (`server.trusted_proxies`,
  `LEOFLOW_SERVER_TRUSTED_PROXIES`). Previously `X-Forwarded-For` was honored
  from any source, so the client IP behind the login rate-limiter and the audit
  log could be spoofed. The client IP is now the direct peer unless you list the
  reverse proxy's IP/CIDR. **Behavior change** for deployments behind an ingress:
  set `server.trusted_proxies` to the ingress CIDR to keep per-client
  rate-limiting and audit accurate. See
  [Configuration → Trusted proxies](docs/configuration.md).
- **`/metrics` is no longer served on the public API/UI listener; `/readyz`
  no longer leaks dependency errors** (audit H2). Prometheus `/metrics` was
  exposed unauthenticated on the API port in addition to the dedicated metrics
  listener; it is now served **only** on the observability listener (the metrics
  port, which every role runs), so it can be firewalled separately from the API.
  And a failing `/readyz` returned the raw dependency error (which can carry a
  DSN, credentials, or internal hostnames) to an unauthenticated caller; it now
  names only the unready dependency and logs the full error server-side. Scrapers
  must target the metrics port (`LEOFLOW_SERVER_METRICS_ADDR`, default `:9090`),
  not the API port.

## [0.2.0] - 2026-08-07

> The **0.2.0** line. The control plane can now run as **separate api and
> scheduler processes** (ADR 0049, `split.enabled`, off by default), the inline
> **`http_api` task type is removed** — closing an SSRF surface (**breaking**
> only for a hand-authored `http_api` DAG) — and **task reaping is now
> at-most-once**: a reaped task's pod is actually torn down instead of running
> user code to completion. Plus a pod-aware dispatch-lost reaper, a configurable
> task namespace, shell-quoted bash templating, and an e2e/chaos harness that
> runs on Linux/Lima, not only Docker Desktop. (Shipped through `v0.2.0-rc.1`,
> validated on a Linux VM and a hands-on UI journey, then promoted unchanged.)

### Changed

- **k3d e2e image import is now verified + retried** (test/e2e reliability).
  `k3d image import` prints "Successfully imported" even when the in-node
  containerd import fails ("tarball: no such file or directory"), so a flaky
  import left the cluster without the image and every task pod ErrImagePulled —
  a confusing downstream failure. A shared `k3d_import` helper treats the import
  as successful only when k3d exits 0 AND emits no error line, retrying up to 3×
  and failing loud otherwise. Applied across the operator, split, and dbt e2es.

- **Runtime chaos / fault-injection e2e harness** (`make chaos-runtime`, #231
  Phase 2). A destructive k3d harness that injects real faults on the two newest,
  riskiest surfaces and asserts the invariants hold: killing the scheduler
  mid-run in the api/scheduler split — the api's `/monitor/health` flips the
  scheduler unhealthy, the in-flight run resumes on restart, and each task ran
  exactly once (at-most-once, no duplicate dispatch) — and force-deleting a
  running task's pod, after which the agent-lost reaper moves the task off
  `running` with no orphaned pod left behind. Not gated in CI (slow); run on a
  Linux box.

- **The k3d e2e harness and `rc-smoke` battery now run on Linux/Lima, not only
  Docker Desktop.** Running the RC battery on a Linux host surfaced four
  environmental gaps that also affect a real Lima gate: task pods reach the host
  control plane via `host.k3d.internal` on Linux (Docker Desktop's
  `host.docker.internal` is not injected there); the DAG image is pinned to the
  host arch (the loader defaults `build.platforms` to `linux/amd64`, which fails
  `FROM` an arm64 base with `InvalidBaseImagePlatform` → `ErrImagePull`); the base
  image is built with `--provenance=false` (a buildx manifest list breaks a later
  `FROM` with "no match for platform"); and `rc-smoke`'s ui-smoke step now brings
  up the Lite control plane it needs and drives it over IPv4. Test-only; macOS
  behaviour is unchanged (the Linux branches are guarded by `uname`).

### Fixed

- **Split api role now reports pod dispatch correctly on `/api/v2/monitor/executor`**
  (ADR 0049 pre-RC review). In split mode the executor runs in the scheduler
  process, so the api role's runtime dispatch flag was always false and the
  endpoint — whose job is "why is a task stuck queued" — told operators pod
  dispatch was off when it wasn't. The api role now reports the configured
  capability (`executor.type`) instead of the in-process bool.

### Fixed

- **Reaping a task now actually stops it** (#474). The three scheduler reapers
  (`agent_lost`, `dispatch_lost`, `orphaned`) only wrote metadatabase state, so a
  reaped task's pod kept running user code to completion — breaking at-most-once
  execution when that work committed or a retry re-ran it. Each reaper now tears
  the pod down **after** the durable DB transition: the per-TI reapers delete
  exactly the reaped `(run-id, task-id, try-number)` pod (a retry's newer pod has
  a different try-number label, so it can never be the one deleted), and the
  orphan-run reaper deletes every pod of the abandoned run. As a belt-and-suspenders
  layer for pods we couldn't delete (e.g. a K8s API outage), the control plane now
  answers a **stale** agent `ReportState`/`Heartbeat` — one whose attempt no longer
  matches the live row — with `should_terminate`, so a reaped-but-alive pod cancels
  its own work. The "stale" test reuses the exact source-state + `try_number` guard
  the state write already carries (#467): a live, matching attempt always applies,
  so a live execution is never torn down or told to stop. Deletion uses only the
  `list`+`delete` pod verbs the executor Role already grants; Lite (subprocess) has
  no pods, so only the DB transition and the terminate signal apply.
- **Dispatch-lost reaper is now pod-aware, ending the cold-node false positive**
  (#461). A slow image pull on a cold node could leave a TI in `queued` past the
  3-minute threshold while its pod was actually `Pending`/`Running`, so the reaper
  failed a live dispatch as `dispatch_lost`. On Kubernetes the reaper now checks
  pod liveness first: if a pod for the TI is `Pending`/`Running` (the dispatch
  landed, the node is just slow) — or if liveness can't be read (K8s API error) —
  it defers instead of reaping. It only fails a TI when no live pod exists. Lite
  keeps the pure time-threshold behavior. New metrics: `dispatch_lost_deferred`,
  `dispatch_lost_pod_query_error`.
- **`taskNamespace` now actually moves the control plane** (#480). The chart
  granted the executor Role in `.Values.taskNamespace` while the server created
  task pods in a hardcoded `leoflow` namespace, so any override installed cleanly
  and then 403'd every dispatch. The namespace is now configuration
  (`executor.task_namespace`, wired from the chart's `taskNamespace` via
  `LEOFLOW_EXECUTOR_TASK_NAMESPACE`), so the server acts on exactly the namespace
  it is granted — the knob means what it says. A helm test asserts the server env
  and the RBAC namespace derive from the same value.

### Security

- **Bash task templating now shell-quotes interpolated values** (issue #489). A
  `bash_command` Jinja-renders the run context, and `params` is the run's `conf` —
  supplied by anyone with `execute:dag`, a lower bar than authoring the DAG. A
  value with shell metacharacters (`x; rm -rf /`, `$(...)`) was interpolated into
  the command string unquoted, so a trigger could run arbitrary commands in the
  task pod (privilege escalation from "may run this pipeline" to "may run any
  command"). Every interpolated value is now `shlex.quote`d before `bash -c`; the
  trusted template text is unchanged and safe values render byte-identically.
  Behavior change: a value can no longer expand into multiple shell words — write
  interpolations unquoted (`--name {{ params.x }}`).

### Added

- **Optional api/scheduler split for Pro (`split.enabled`, off by default; ADR
  0049).** `leoflow-server` gains a role (`LEOFLOW_SERVER_ROLE`: `all` (default,
  and Lite's only mode — behavior-identical to before), `api`, `scheduler`). When
  the chart's `split.enabled` is set, it renders a restricted-identity api
  Deployment (HTTP + UI, active-active, its ServiceAccount unbound from the
  pod-create Role) and a privileged single-leader scheduler Deployment (reconciler
  + dispatch + agent gRPC), each with its own Service/RBAC/NetworkPolicy and
  role-appropriate probes. Isolates API load from scheduling and shrinks the
  API's blast radius. Lite and existing monolith installs are unaffected.

### Changed

- **The metrics listener (`:9090`) now also serves `/healthz` and `/readyz`**, and
  is scoped to those plus `/metrics` (a request to another path returns 404,
  where the bare Prometheus handler previously answered on any path). This gives a
  scheduler-only pod a probe target (ADR 0049) and is additive for the standard
  `/metrics` scrape; adjust any scrape configured against `:9090/` (non-`/metrics`).

### Removed

- **The native inline `http_api` task type is removed** (ADR 0047/0048, issue
  #512). It ran an author-supplied HTTP request *inline in the control-plane
  process*, carrying the control plane's network position — a server-side request
  forgery surface (audit finding H5). Registering a spec with `type: http_api` is
  now **rejected** at validation (the parser already stopped emitting it — an
  `HttpOperator` compiles to a pod-run `airflow_operator`, ADR 0040 — so it could
  only arrive via a hand-written `dag.json`), and the inline executor and its
  scheduler wiring are deleted, so the guard is structural (no in-process path to
  route to, ADR 0048), not an input check. **Breaking** only for a hand-authored
  `dag.json` that declared `http_api`; migrate to an `HttpOperator` (runs in a
  task pod; declare `connectors: [http]`). The one-release deprecation window was
  collapsed into this release because it is the first Pro-facing cut and shipping
  the SSRF was not acceptable; the parser-emitted path was already gone, so no
  compiled DAG is affected.

### Added

- **A task-pod egress NetworkPolicy** (`taskNetworkPolicy`, off by default). Task
  pods run untrusted author code, so this is where the "a pod can reach cloud
  metadata / the apiserver / another service" risk is contained — at the network
  layer, for every task type — not in the control plane (ADR 0048). When enabled
  it denies ingress, allows DNS and the control-plane gRPC (the agent dials back),
  then all other egress **except the cloud metadata endpoint** (`169.254.0.0/16`),
  which is always blocked — the one unambiguous SSRF target. `blockPrivateNetworks`
  additionally denies RFC1918 + the apiserver; it is opt-in because a DAG calling
  an internal service is legitimate and the policy cannot tell that from the
  apiserver by IP (ADR 0047). This is the network-layer control ADR 0047/0048 point
  to, and the same one Argo and Kubeflow recommend.

### Added

- **A synchronous dispatch failure now backs off and eventually gives up, instead
  of retrying every tick forever** (ADR 0031 Amendment A). When `Dispatch` failed
  synchronously — kube-apiserver unreachable, RBAC denied, quota, an admission
  webhook reject — the task stayed `scheduled` and the planner re-attempted it on
  every tick, with no backoff, surfaced by no reaper (the dispatch-lost reaper
  only sees `queued`). A permanent misconfiguration became a silent tight loop.

  The task instance now records `dispatch_attempts` and `next_dispatch_at` (two new
  columns, mirroring `reschedule_at`); the planner does not re-dispatch until the
  exponential, capped backoff elapses, and after the attempt budget is spent the
  task fails as `dispatch_failed` so the run finalizes. A dispatch failure does
  **not** consume the task's `try_number` — it is infrastructure, not a task
  failure, so a `retries: 0` task is not killed by a transient blip.
  `dispatch_failed` is distinct from `dispatch_lost` (dispatched then vanished) and
  from a task's own `failed` (the code ran and failed).

- **`leoflow compile` now rejects a task graph that cannot execute.** Three
  defects shared one symptom, and it was the worst one available: the run
  started, no task in the affected region ever became ready, and the run sat in
  `running` indefinitely with nothing on screen explaining why. There was no
  error to read — from the scheduler's side nothing had gone wrong, it was
  waiting on a predecessor, correctly, forever.

  - a **cycle** (`a >> b >> c >> a`, or a self-dependency). Airflow does not
    reject this: the parser emits a perfectly well-formed `dag.json`.
  - a **`depends_on` naming a task that is not declared** — the more common typo.
  - a **duplicated `task_id`**, which is the same family for a different reason:
    the graph keys by id, so the losing definition was silently dropped and the
    DAG ran a subset of what was written.

  The error names the tasks involved (`cyclic task graph: a -> c -> b -> a`),
  because "cycle detected" alone leaves the author to find it by hand in a
  200-task DAG. Cycle detection is a three-color DFS rather than a visited set: a
  visited set reports a diamond — two paths rejoining at one task, one of the most
  ordinary shapes a real DAG has — as a cycle. Traversal follows declared task
  order, so the same DAG always reports the same cycle.

  Validated at compile and again at registration, so a hand-written or
  machine-generated `dag.json` cannot bypass the compiler.

- **A typo in an alert template is now a compile error.** An unknown placeholder
  is not a rendering failure — `Render` leaves it alone — so `{{taskk}}` used to
  survive compile and reach the operator verbatim, discovered in the alert that
  was supposed to explain an outage. `leoflow compile` now rejects it, naming the
  offending placeholder and listing the supported set. Airflow-style names
  (`{{ ds }}`) are reported by name rather than passing through as text.

- **`leoflow compile` now rejects a `dbt.project` or `dbt.manifest` path it cannot use.** The value
  is resolved with `filepath.Join(dagDir, project)` and, for a Pro image build,
  baked at that same relative path inside the image — so both an absolute path and
  one escaping upward were broken, and broken silently.

  `filepath.Join` does not treat an absolute second element specially:
  `Join("/dags/sales", "/opt/dbt/proj")` is `/dags/sales/opt/dbt/proj`. The
  leading slash was swallowed and dbt was pointed at a directory nobody named. A
  path like `../../../etc` resolved outside the DAG directory, and therefore
  outside the Docker build context, so the image could not contain it either.

  Both fields feed the same `filepath.Join` chain — `project` onto the DAG
  directory, then `manifest` onto that result — so both are checked. Validating
  only `project`, as the first version of this change did, left half the defect in
  place.

  Both surfaced only when dbt ran inside a pod, as "project directory does not
  exist", with nothing connecting it back to `leoflow.yaml`. The error now names
  the field, what it resolves to, and why that cannot work. `.`, `transform`,
  `./transform`, `dbt/transform`, `target/manifest.json` and paths that normalise
  back inside are unaffected.

### Fixed

- **The pod reconciler could garbage-collect a failed pod before its failure was
  recorded.** `Reconcile` reported a failed pod's task instance and then deleted
  the pod if it had aged out — but the delete ran whether or not the report
  succeeded. A transient metadatabase error during `FailTask` meant the pod (the
  only signal that would let the next tick retry) was deleted anyway, stranding
  the task instance in `running` until the slower heartbeat reaper caught it. The
  reconciler now defers collection of a failed pod until its failure is durably
  recorded; a succeeded pod, which has nothing to record, is still collected on
  age alone. (One component was both the state-recorder and the garbage-collector
  with no ordering between them.)

- **The pod reconciler and staging-volume GC ran on every replica, not just the
  leader.** The scheduler loop and its reapers are leader-gated (ADR 0009), but
  `startReconciler` and `startStagingGC` spawned unconditional tickers, so at
  `replicaCount > 1` every replica would list, reconcile, and delete the same task
  pods and staging PVCs — a follower racing the leader's provisioning. Both now
  gate on the same leadership signal (`scheduler.IsLeading`) the scheduler loop
  uses. No behaviour change at the default `replicaCount: 1`, where the single
  replica is always the leader; this is the correctness base that makes running
  Pro with more than one replica safe.

- **Alert messages carried a UUID where the run id belongs.** `{{run_id}}`
  rendered `RunState.RunID`, which is the `dag_runs` primary key — not the
  `run_id` an operator sees in the UI and passes to the API. The alert named a
  run nobody could look up. It now renders the user-facing id.

- **`{{logical_date}}` always rendered empty.** The placeholder was documented and
  substituted, but the dispatcher never populated the field, so every alert using
  it produced a dangling `for `. `RunState` now carries the logical date and the
  dispatcher passes it through.

- **A placeholder with no value renders `(none)`** instead of an empty string —
  `{{logical_date}}` on a manually triggered run, for example. `failed for
  logical date ` is indistinguishable from a truncated message.

### Security

- **Hardened the log sink against path escape.** `DiskSink` interpolated every
  `logs.Ref` field straight into a filesystem path
  (`{root}/{tenant}/{dag}/{run}/{task}/{try}.log`) with no containment, so any
  Ref carrying `../` or a separator read and wrote outside the log root. Both
  filesystem calls were annotated
  `//nolint:gosec // path is built from validated identity fields`, asserting a
  validation step that existed nowhere in `internal/` — which is why static
  analysis stayed quiet on those lines for the life of the repository.

  **Not reachable in any released version.** All five `logs.Ref` construction
  sites pass the database UUID as `RunID`: the scheduler builds `RunState.RunID`
  from `uuidToString(run.ID)`, dispatch mints that same value into the agent
  token, and both read paths resolve the caller's `run_id` through
  `ResolveRunRef` to `ref.DagRunID` before touching the sink. `dag_id` and
  `task_id` carry a charset pattern in the DAG schema, enforced at compile and
  again at registration. No release shipped an exploitable path. What shipped was
  a sink whose safety rested entirely on every current caller happening to pass a
  UUID, under a comment claiming a guarantee that was absent.

  The sink now performs all filesystem work through `os.Root` pinned to the log
  directory, so escape is refused by the runtime rather than by convention. That
  also closes a case no string check can see: a symlink inside the log root whose
  every path component is a legal name, where the traversal happens during kernel
  resolution. `logs.Ref` is additionally validated field by field, which names the
  offending field in the error.

  Separately, `POST /api/v2/dags/{dag_id}/dagRuns` accepted `dag_run_id` verbatim
  from the request body with no validation at all. It now rejects a value that is
  not a usable single path segment. Airflow-generated ids embed an RFC3339
  timestamp and keep working: separators are banned, punctuation is not.

## [0.1.2] - 2026-07-30

> **On-failure alerting, and a Pro install that can no longer be misconfigured into
> silence.** A failed task notifies on its own, without an Airflow callback in the
> loop; and three Pro misconfigurations that used to produce a healthy-looking but
> broken deployment now fail loudly at `helm install` or at boot.
>
> This promotes `v0.1.2-rc.2` unchanged. The candidate was exercised by hand on
> darwin/arm64 — the platform no CI job covers — including the alerting paths, both
> Pro guards, and the callback fix that prompted the respin. Per-candidate detail is
> in the `0.1.2-rc.2` and `0.1.2-rc.1` sections below.

### ⚠️ Upgrade note for Pro operators

`agentTLS.enabled: false` no longer yields a running deployment — it never yielded a
working one, and now says so at install time instead of crash-looping. Full detail and
the migration in the `0.1.2-rc.1` section below.

### Known issues carried into this release

- **A task can be marked `dispatch_lost` while its pod is still `Running` (#461).** The
  mechanism is now understood (#474): no reaper consults Kubernetes, nothing deletes the
  pod, and the `should_terminate` signal the agent honours is never sent. A slow image
  pull is enough to trigger it. Fix in progress.
- **Shutdown can hang when the Kubernetes API stops answering (#463).** Described in the
  `0.1.2-rc.2` section; the fix is written and lands next.

## [0.1.2-rc.2] - 2026-07-30

> Respin of `0.1.2-rc.1` for one defect, found by hands-on validation of that
> candidate: a task declaring `on_failure_callback` as a **list** — the shape Airflow
> itself produces — had it dropped silently. Nothing else changed; every other item
> in the 0.1.2 line is described in the `0.1.2-rc.1` section below and was verified
> against that candidate.

### Fixed

- **`on_failure_callback` written as a list now runs (#470).** Airflow 3 normalises a
  task's callback attributes to a list, so `@task(on_failure_callback=[notify])` — and
  any DAG copied from a real Airflow deployment — carries `[fn]`, not `fn`. The runtime
  already accepted both shapes; the compiler's gate tested only `callable()`, so the
  list form produced no marker in `dag.json`, no warning, and no callback at run time.
  A bare callable was unaffected.
- **An *unsupported* callback written as a list is loudly rejected again (#470).** The
  same gate guards the compile-time refusal for `on_success_callback` and
  `on_retry_callback`. In list form they slipped past it and were dropped in silence —
  the precise outcome the refusal exists to prevent, and worse than the case it was
  written for, because the author was told nothing.

### Known issues

- **Shutdown can hang when the Kubernetes API stops answering (#463).** The 0.1.2
  line wires the buffered dispatch drain into shutdown (#133) so in-flight dispatches
  settle instead of leaking — but the wait is unbounded and the Kubernetes client
  carries no per-call timeout. An apiserver that accepts the connection and never
  answers pins a worker, and with it the drain, until the runtime kills the process.
  In that specific case shutdown is worse than in 0.1.1, where the drain never ran at
  all. The fix is written and held for the next release rather than folded in here,
  to keep this candidate a targeted respin. Workaround: none needed unless your
  apiserver hangs; the pod is SIGKILLed at the end of its termination grace period.
- **A task can be marked `dispatch_lost` while its pod is still `Running` (#461)** —
  carried over from rc.1, still under investigation.

### Corrected from the rc.1 notes

The `0.1.2-rc.1` section claims, under Fixed, that `on_failure_callback` runs for
"list-normalised callbacks". Unbound DAGs did work; **lists did not**. Tags are
immutable (ADR 0033), so that section stands as published — the claim it makes is true
of this candidate, not of rc.1.

## [0.1.2-rc.1] - 2026-07-30

> First release candidate of the **0.1.2** line — **native on-failure alerting** and a
> **Pro install that can no longer be misconfigured into silence**. A failed task now
> notifies on its own, without an Airflow callback in the loop; and the three Pro
> misconfigurations that used to produce a healthy-looking-but-broken deployment now
> fail loudly at `helm install` or at boot.

### ⚠️ Upgrade note for Pro operators

**`agentTLS.enabled: false` no longer yields a running deployment.** It was never a
plaintext deployment — the control plane marks itself as the Pro edition and every
secrets RPC to a task pod was rejected, so tasks queued and hung with no visible
cause. That failure is now surfaced where you can act on it:

- `helm install`/`upgrade` **refuses to render** with `agentTLS.enabled=false`.
- The control plane **refuses to boot** without `LEOFLOW_SERVER_GRPC_TLS_CERT`/`_KEY`
  when `LEOFLOW_UI_EDITION=pro`.

**If your values override `agentTLS.enabled` to `false`,** provision a cert before
upgrading — cert-manager `Certificate` + CA trust bundle, then set
`agentTLS.serverCertSecret` and `agentTLS.caConfigMap`. Step-by-step in
[`docs/pro-tls.md`](docs/pro-tls.md). Installs on the `agentTLS.enabled: true` default
(unchanged since 0.1.1) are unaffected. For a plaintext local loop, use the Lite dev
server (`leoflow dev lite`), which is not subject to the Pro guards.

### Added

- **Native on-failure alerting (#424).** A DAG declares its alert targets and the
  control plane notifies on final task failure from a Go notifier — no Airflow
  callback in the request path. Configuration, `dag.json` fields and the notifier
  ship together; see [`docs/alerting.md`](docs/alerting.md).
- **Airflow `on_failure_callback`, in-process on final failure (#424).** The familiar
  Airflow hook runs where Leoflow already knows the task reached its terminal state,
  so existing DAG code keeps working alongside native alerting.
- **Alerts dedup per failure episode (#431).** Retries within one failure episode
  produce one notification, not one per attempt.
- **Saturation-drop metric (#435).** Drops caused by a saturated alert path are now a
  metric you can alert on, not just a log line.

### Changed

- **The Pro edition refuses to boot without TLS on the agent gRPC channel (#281).**
  Booting looked healthy while every secrets RPC failed; it now fails at boot with the
  reason. See the upgrade note above.
- **The Helm chart fails at render on the silent Pro misconfigs** — `agentTLS.enabled`
  with an empty `caConfigMap` (#280), a `ReadWriteOnce` logs PVC under more than one
  replica whether static or HPA-driven (#282), and `agentTLS.enabled=false` on a chart
  that only ever deploys Pro (#459). Each fails in about a second with an actionable
  message instead of a `CrashLoopBackOff` or a `Multi-Attach` hang.

### Fixed

- **Buffered dispatch drains on shutdown, and `Close()` is no longer racy (#133).**
  In-flight dispatches are no longer dropped when the control plane stops.
- **The parser rejects an oversized literal task argument at compile time (#149)**,
  instead of failing later in the pod.
- **The parser captures `retries`, `retry_delay` and `execution_timeout` from
  operators (#434)** — previously dropped, so a task silently ran with defaults.
- **`on_failure_callback` runs for unbound DAGs and list-normalised callbacks (#424).**

### Security

- **grpc → v1.82.1**, clearing `GHSA-hrxh-6v49-42gf`.
- **`x/net` and `x/text` bumped**, clearing `CVE-2026-46600` and `CVE-2026-56852`.
- **Trivy filesystem scan now runs on pull requests (#437)**, not only on push and
  schedule, so a vulnerable dependency is caught before merge.
- Nine further dependency bumps in the minor-and-patch group.

### Known issues

- **A task can be marked `dispatch_lost` while its pod is still `Running` (#461).**
  Observed once in CI and not reproducible on rerun; the pod is dispatched and alive,
  but its agent never reports in, and the control plane fails the task at the dispatch
  threshold. In production this risks a false failure (and its alert) on work that may
  still be executing. Under investigation — please report occurrences with the run id.

## [0.1.1] - 2026-07-10

> **dbt-native orchestration.** A dbt project becomes a Leoflow DAG — one pod per
> model, no Cosmos at runtime, no Airflow in the parser — and develops **locally with
> zero config** against an embedded duckdb. This promotes `v0.1.1-rc.1` after a clean
> Lite (arm64) end-to-end soak, plus a Go 1.26.5 toolchain bump for a newly-disclosed
> standard-library advisory (below) — no functional change from the rc.

### Security

- **Go toolchain 1.26.4 → 1.26.5**, closing `GO-2026-4970` (root escape via symlink +
  trailing slash in the standard-library `os` package), disclosed after the rc was cut
  and reachable from the installer's download path.

### The 0.1.1 line, in brief

- **dbt projects as Leoflow DAGs (ADR 0042).** A dbt project compiles straight to
  Leoflow tasks from its `manifest.json`, in Go — one task per model/seed/snapshot/test,
  wired by dbt's own dependency graph.
- **Mix dbt with operators (ADR 0043)** via `dbt_group("name")`, and **multiple dbt
  projects, one per business domain (ADR 0044)** via a namespaced `dbt_groups` map.
- **Adapters:** Postgres, Snowflake, BigQuery, Databricks, and **duckdb** — mapped from a
  managed connection at runtime, so no warehouse credential is baked into the image.
- **Zero-config local dbt (Lite):** a dbt project with no connection and no
  `profiles.yml` just runs against an embedded duckdb, never touching your global
  `~/.dbt`; model edits hot-reload.

Per-rc detail is in the `0.1.1-rc.1` section below.

## [0.1.1-rc.1] - 2026-07-05

> First release candidate of the **0.1.1** line — **dbt-native orchestration**. A dbt
> project becomes a Leoflow DAG (one pod per model, no Cosmos at runtime, no Airflow in
> the parser), and — new this line — a dbt project develops **locally with zero config**.

### Added

- **dbt projects as Leoflow DAGs (ADR 0042).** A dbt project compiles straight to
  Leoflow tasks from its `manifest.json`, in Go — one task per model/seed/snapshot/test,
  wired by dbt's own dependency graph. No Cosmos at runtime, no Airflow in the parser.
- **Mix dbt with operators (ADR 0043).** `dbt_group("name")` embeds a dbt project as a
  namespaced task group inside a normal `dag.py`, wired around your operators/sensors;
  `granularity` (node/level/folder) is a pod-packing knob.
- **Multiple dbt projects, one per business domain (ADR 0044).** A `dbt_groups` map lets
  one DAG carry several domain projects (`sales__*`, `marketing__*`), namespaced so
  identically-named models never collide.
- **Adapters:** Postgres, Snowflake, BigQuery, Databricks (the official `dbt-databricks`
  adapter), and **duckdb** — Leoflow maps a managed connection to each adapter's profile
  at runtime, so no warehouse credential is baked into the image.
- **Zero-config local dbt (Lite).** `leoflow lite` + a dbt project with no connection and
  no `profiles.yml` just runs — against an embedded **duckdb** file, generated
  transparently at compile and run time, never touching your global `~/.dbt`. Model
  edits hot-reload (the manifest re-parses with the per-DAG venv's dbt).

### Fixed

- **Lite runs dbt end-to-end (subprocess):** the per-DAG venv's `bin` is now on the
  task's PATH (so a bare `dbt` resolves), the dbt `--project-dir` is absolute for local
  builds (so the project resolves from the task workdir), and the manifest parses with
  the venv's dbt — the three gaps that stopped `leoflow lite` from running a dbt DAG.
- **`leoflow compile` self-heals the extracted parser after a binary upgrade** (#239), so
  a new build's features (like dbt) never fail against a stale `~/.leoflow/pysrc`.

## [0.1.0] - 2026-06-28

> **First stable release.** Leoflow runs standard Apache Airflow 3.2 DAGs on a Go
> control plane — no GIL, no Airflow in the scheduling path — one pod per task. This
> promotes `v0.1.0-rc.4` verbatim: the same artifacts, soaked through the rc series.

### The 0.1.0 line, in brief

- **Standard Airflow 3.2 DAGs in Python.** A dependency-free structural shim (ADR
  0024) parses `airflow.sdk` DAGs without importing Airflow; the real provider
  operator runs in the task pod (ADR 0040).
- **Provider operators & sensors.** A native fast path for `bash`/`python`/`http`,
  generic capture for the long tail, and reschedule-mode sensors.
- **86 connection types** generated from real Airflow (ADR 0038 / 0039), with the
  `connectors:` one-liner that bakes providers into each DAG's image.
- **Lite** — a Docker-free, Kubernetes-free local edition: hot-reload, the embedded
  Airflow 3.2 UI, and resilience (Docker-wedged fallback, boot self-heal, per-DAG
  venv reclaim, watcher-token refresh).

Per-rc detail is in the `0.1.0-rc.1` … `0.1.0-rc.4` sections below.

## [0.1.0-rc.4] - 2026-06-28

> Fourth release candidate of the **0.1.0** line — a single Lite fix found while
> testing rc.3.

### Fixed

- **Lite hot-reload no longer stops registering after an hour (#407).** `leoflow
  lite` minted one admin token at startup (one-hour expiry) and reused it for every
  hot-reload registration, deregister, and the boot reconcile; after an hour the
  token expired and every save silently failed to register — only a `✗ … invalid
  token` in the log, with the UI just not updating. The watcher now re-mints a fresh
  token per operation, so a Lite left running for the day keeps reloading.

## [0.1.0-rc.3] - 2026-06-28

> Third release candidate of the **0.1.0** line. On top of rc.2 it hardens the
> **install path** and makes the local **Lite** edition **resilient** — every item
> here surfaced during hands-on testing of rc.2. The boot self-heal is end-to-end
> gated in CI.

### Added

- **Lite self-heals stale state on boot (#404).** A reused metadata DB no longer
  leaves "ghost" DAGs and orphan import errors that the UI showed but could not
  remove: on startup Lite reconciles the registered DAGs against the workspace —
  deregistering what is gone on disk and clearing stale import errors — fail-safe
  (if the control plane can't be listed, it wipes nothing). A new end-to-end gate
  (`lite-selfheal`) keeps it from regressing.
- A DAG's **per-DAG venv is reclaimed when the DAG is deregistered** (and logged),
  instead of lingering on disk with the Airflow SDK; a later reload re-creates it
  if the DAG returns. The sweep of venvs orphaned while Lite was stopped is tracked
  for a scheduled GC (#406).

### Fixed

- **Install:** the pinned-version command placed `LEOFLOW_VERSION` on `curl`
  instead of `sh`, so `install.sh` resolved latest-stable and installed the
  previous release rather than the pinned rc. The variable now sits on the `sh`
  side of the pipe (#402).
- **Lite falls back to a Docker-free Postgres when Docker is wedged (#403).** The
  auto-resolvers now ping the Docker daemon; a present-but-unresponsive Docker
  (e.g. a hung Docker Desktop returning 500s) falls back to the managed Postgres
  and the subprocess executor instead of aborting on `docker compose up`.

## [0.1.0-rc.2] - 2026-06-24

> Second release candidate of the **0.1.0** line. On top of rc.1 it ships
> **reschedule-mode sensors** — the first ADR 0040 Phase B capability, which rc.1
> still rejected at compile — plus release- and CI-hardening fixes. E2E-gated on a
> real k3d cluster.

### Added

- **Reschedule-mode sensors (ADR 0040, Phase B).** A sensor declared
  `mode='reschedule'` now releases its pod between pokes instead of holding it:
  on a not-ready poke the task transitions to `up_for_reschedule` and the
  scheduler re-dispatches it once `reschedule_at` arrives — no retry budget
  consumed (mirrors the `up_for_retry` rail). The agent reports
  `up_for_reschedule` only when the task exits 75 **and** writes a parseable
  reschedule file, so a bare exit 75 stays an ordinary failure. Validated on a
  real k3d cluster via a `DateTimeSensor(mode='reschedule')` e2e guard that
  visibly passes through `up_for_reschedule` and is re-dispatched to success
  (#380, #389).

### Fixed

- Deterministic reschedule-sensor e2e guard: the wait loop asserts the sensor
  passed through `up_for_reschedule` without timing flakiness (#390).
- Release notes now install the exact release tag (`LEOFLOW_VERSION` + the
  tag's `install.sh`) instead of resolving latest-stable, and drop the stale
  `(pre-alpha)` wording (ADR 0037) (#391).

### Changed

- CI secret-scan runs a pinned `gitleaks` binary and drops the flaky Docker Hub
  pull (#392).

## [0.1.0-rc.1] - 2026-06-16

> First release candidate of the **0.1.0** line — a Go control plane that runs
> DAGs end-to-end and serves the embedded Apache Airflow 3.2.1 UI. Published as a
> pre-release after hands-on maintainer validation of the UI and the connector
> flow in a real browser.

### Added

- **Embedded Airflow 3.2.1 UI (Phase 5).** The control plane embeds the pinned
  Airflow 3.2.1 React SPA (`go:embed`, ADR 0017) and serves it at `/`, alongside
  the implemented internal UI API:
  - Auth/identity: `GET /ui/config`, `GET /ui/auth/me`, `GET /ui/auth/menus`
    (curated to the screens Leoflow backs), `POST /ui/auth/token`.
  - Read views: `GET /ui/dags` (latest runs embedded, no N+1), `/ui/dags/{id}/latest_run`,
    `/ui/grid/runs/{id}`, `/ui/grid/structure/{id}`, `/ui/structure/structure_data`,
    `/ui/grid/ti_summaries/{id}` (NDJSON stream with a conditional-GET ETag),
    `GET /api/v2/dags/{id}/details` (cron→English), `GET /api/v2/version`.
  - Graceful degradation: unimplemented `/ui` screens return schema-valid empty
    responses; writes degrade to `501`.
  - Static assets are gzipped; the SPA shell and assets load without auth so the
    login screen is reachable, while `/api/v2` and `/ui` data stay gated.
- **One-command demo.** `docker compose --profile demo up --build` brings up
  Postgres, Redis, and the control plane with the UI; bootstraps an admin user.
  `deploy/Dockerfile.server` builds the single image.
- `make fetch-airflow-ui` extracts the pinned UI bundle from `apache/airflow:3.2.1`.
- **Connectors — provider hooks/operators without `apache-airflow` in the control
  plane (ADR 0038/0039).** `connectors:` / `dependencies:` in `leoflow.yaml` install a
  provider into the DAG image; a **generated connection catalog** (86 connection
  types, derived from real Airflow) drives the UI's Add-Connection form
  (`/ui/connections/hook_meta`) and the structural connection-test probe
  (`POST /api/v2/connections/test`). Admin Variables and Connections are delivered to
  each task as `AIRFLOW_VAR_*` / `AIRFLOW_CONN_*` (encrypted at rest, ADR 0019) and
  resolved by Airflow's native env-secrets backend — in pods (via the agent over gRPC)
  and in Lite/subprocess.
- **Airflow operators & poke sensors (ADR 0040, Phase A).** Any provider operator or
  sensor runs in its own pod through a generic executor
  (`import_string(class)(**args) → render_template_fields → execute(context)`) — no
  per-operator code. Includes `ti.xcom_pull` chaining between operators; the
  standalone run context (`ds`, `ts`, `data_interval_*`, `params`, `var`, `conn`);
  operator extra-links (the UI "open in …" buttons); multi-key XCom; and native
  `@task` / `bash` parity (including Jinja-templated bash commands). Reschedule-mode
  sensors, deferrable operators, dynamic task mapping and branching are loudly
  rejected with actionable errors (tracked for later phases).

### Changed

- ADR 0007 (Airflow UI Compatibility) premise refined from Airflow 2.x-style
  `/api/v2` parity to the pinned Airflow 3.x `/ui/*` approach (see ADR 0017,
  ADR 0018, `docs/ui-compatibility.md`).

### Fixed

- The static SPA (shell + assets) is now public, so an unauthenticated first
  visit can load the app and reach the login screen.
- `/ui/auth/me` returns the authenticated user's username (the JWT now carries
  the email claim).

### Notes

- The pinned Airflow UI is a tactical MVP choice; a purpose-built Leoflow UI on
  the stable `/api/v2` is the long-term direction (ADR 0018).
- Browser end-to-end verification (rendering, write-flow paths, screenshots) is
  the remaining Phase 5 acceptance step; see `docs/ui-compatibility.md`.

[Unreleased]: https://github.com/neochaotic/leoflow/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/neochaotic/leoflow/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/neochaotic/leoflow/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/neochaotic/leoflow/compare/v0.1.2-rc.2...v0.1.2
[0.1.2-rc.2]: https://github.com/neochaotic/leoflow/compare/v0.1.2-rc.1...v0.1.2-rc.2
[0.1.2-rc.1]: https://github.com/neochaotic/leoflow/compare/v0.1.1...v0.1.2-rc.1
[0.1.1]: https://github.com/neochaotic/leoflow/compare/v0.1.1-rc.1...v0.1.1
[0.1.1-rc.1]: https://github.com/neochaotic/leoflow/compare/v0.1.0...v0.1.1-rc.1
[0.1.0]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.4...v0.1.0
[0.1.0-rc.4]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.3...v0.1.0-rc.4
[0.1.0-rc.3]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.2...v0.1.0-rc.3
[0.1.0-rc.2]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.1...v0.1.0-rc.2
[0.1.0-rc.1]: https://github.com/neochaotic/leoflow/releases/tag/v0.1.0-rc.1
