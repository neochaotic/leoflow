---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0060-external-secrets-resolution.html
# --- end AUTO redirect aliases ---
title: "ADR 0060: External secrets resolution — a pod-side, provider-neutral SecretResolver (AWS reference)"
linkTitle: "0060 · External secrets resolution — pod-side, provider-neutral SecretResolver (AWS reference)"
weight: 600
description: "ADR 0060: External secrets resolution — a pod-side, provider-neutral SecretResolver (AWS reference)"
---

**Status:** Accepted
**Date:** 2026-08-31
**Accepted:** 2026-08-31
**Relates:** ADR 0021 (exposing variables/connections to pods — the env-export path this extends), ADR 0035 (keyless-first; "leoflow is not a key manager" — reference a platform-managed secret), ADR 0045 (declared secret delivery), ADR 0055 (secret scoping + token liveness — the declaration-scope + liveness gate this composes with), ADR 0048 (no user code/network in the control plane — the hard constraint that fixes the resolution locus), ADR 0014 (supply-chain security — the SDK-in-agent call), ADR 0056 (task-log object sink — the one existing core-side cloud-SDK carve-out, cited as precedent and boundary)
**Issues:** #811 (external secrets resolution; provider-neutral, AWS reference)
**Supersedes:** the "feeds ADR 0036" line in #811 — 0036 is the runtime compatibility shim, not this.

> **Numbering note.** The design studies (`spec/external-secrets-*.md`) and #811
> proposed "ADR 0057" for this work. Between the study and this record, 0057 was
> assigned to OIDC/SSO, 0058 to warm-worker pools, 0059 to OpenLineage. This ADR
> takes the next free number, **0060**; it is the same decision the studies call
> 0057.

## Context

A Connection or Variable in leoflow lives in the **control-plane vault**,
encrypted at rest (ADR 0019), created through the Airflow-compatible UI/API. It
reaches a task **pod-side, over the ADR 0055 exchange** — never as a pod-spec
field. The agent, before user code, calls `GetVariables` / `GetConnections` and
exports `AIRFLOW_VAR_<KEY>` / `AIRFLOW_CONN_<ID>` into the task process
environment (`internal/agent/runner.go`, `secretsEnv`); the RPC handlers resolve
the caller's task from its token, gate on TLS + liveness, and return either the
whole tenant vault (permissive/off) or the declared subset (enforce)
(`internal/agentrpc/secrets.go`). The declared set rides `leoflow.yaml`
(`variables:` / `connections:`, DAG-level and per-task) and is threaded to the
agent-facing spec (`internal/storage/agent_store.go`).

**The gap (#811).** A platform that provisions every secret in an external store
— AWS Secrets Manager via Terraform, GCP Secret Manager, Azure Key Vault,
HashiCorp Vault — must **re-create** each Connection/Variable inside leoflow's
vault to use it. That duplicates the secret (two sources of truth), breaks the
"secret value never in git / lives only in the store + IaC state" property, and
adds a bootstrap step. Apache Airflow solves this with a pluggable **secrets
backend** (`AIRFLOW__SECRETS__BACKEND` + `backend_kwargs`) resolved in the
worker; leoflow has no equivalent.

**The constraints any design must honor:**

- **ADR 0048 — no user-influenced code or network in the control plane.** A
  control-plane process reaching `arn:aws:secretsmanager:…<author-named>` with
  core's identity is precisely the confused-deputy/SSRF class 0048 forecloses in
  advance. This **fixes the resolution locus off the control plane** before any
  other trade-off is considered.
- **ADR 0035 — keyless-first; leoflow is not a key manager.** Credential
  resolution belongs in the task, under the pod's own workload identity
  (`execution.service_account` → IRSA / GKE WI / Azure WI / Vault k8s-auth), not
  in core. §7 already generalizes the stance to AWS/Azure.
- **ADR 0055 — scope by declaration + token liveness.** Delivery is scoped to
  what a DAG **declares**; `secret_scoping: enforce|permissive|off` and the
  liveness gate are operator policy, never author-settable. An external path must
  not silently re-open the whole-vault blast radius 0055 exists to close.
- **Coverage of non-operator tasks.** An in-pod Airflow backend fires **only**
  when code calls `BaseHook.get_connection` / `Variable.get`. leoflow tasks are
  not only operators: python/`@task` and **bash** tasks receive
  `AIRFLOW_VAR_*` / `AIRFLOW_CONN_*` as plain env (`secretsEnv`). A bash task
  reading `$AIRFLOW_CONN_DB` gets nothing from an in-pod backend. Whatever we
  choose must keep feeding the env-export path, which is the only mechanism that
  reaches every task type.

Full market benchmark (Airflow `BaseSecretsBackend`, ESO, CSI, Prefect, Dagster),
the leoflow-fit analysis, and the model comparison are in
`spec/external-secrets-design-study.md`; the AWS reference design + security
review are in `spec/external-secrets-aws-reference-design.md`. This ADR records
the decision those studies reached. Airflow-compatibility is treated as
**desirable, not critical** — a bonus that falls out of reusing in-pod Airflow
backends as an adapter, not the driver of the shape.

## Decision

Ship external secrets as a **two-layer** answer:

**Layer 0 (available today, docs-only — already shipped).** ESO / CSI Secret
Store sync into a K8s Secret, mounted read-only into the task pod via the existing
`taskSecret` mount, referenced from a Connection by `key_path` (ADR 0035). This
is the immediate, zero-code escape hatch (see `operate/external-secrets.md`) and
the permanent floor for providers/edge cases the resolver does not yet adapt. It
is delegation, not a resolution path leoflow maintains.

**Layer 1 (this ADR — the feature).** A leoflow-native, provider-neutral
`SecretResolver`, resolved **pod-side in the agent**:

1. **Resolution locus = pod-side agent, never the control plane** (ADR 0048). The
   Go compiler and DAG registration stay **structural-only** — no provider call
   at compile or registration time (see D6 below).

2. **A provider-neutral `SecretResolver` port** (new package, e.g.
   `internal/agent/secretsource`) with an **AWS reference adapter**. The port
   resolves **one leoflow-declared `conn_id`/`var` name** to a value:

   ```go
   type Kind int
   const ( KindConnection Kind = iota; KindVariable )

   type SecretResolver interface {
       // found=false is a clean miss (fall through the chain).
       // err != nil is a hard failure (fail closed for a required name).
       Resolve(ctx context.Context, name string, kind Kind) (value string, found bool, err error)
   }
   ```

   Provider-neutrality guarantees (the port must not leak AWS assumptions):
   - **Reference form.** Authors declare a plain `conn_id`/`var` **name**, never a
     provider path/ARN/mount. The `<prefix>`/path/region/mount convention lives in
     the **adapter's** `backend_kwargs`, set by the **operator**. An AWS
     `connections_prefix`, a Vault `mount/path#key`, or a GCP
     `projects/<p>/secrets/<name>/versions/latest` never reaches the DAG.
   - **Value shape.** `KindConnection` returns a rendered Airflow URI (the same
     shape leoflow already produces, `airflowConnURI`); `KindVariable` a scalar.
     The **adapter** owns JSON-blob-vs-plaintext-vs-map normalization before
     returning — no "it's probably JSON" assumption in the port.
   - **No rotation loop.** Pods are per-task (ADR 0002); the resolver reads once at
     task start, fresh for that attempt. Rotation is the provider's job.

3. **Resolution chain: declared name → external adapter → leoflow vault → env**,
   composed **inside** `secretsEnv` (not around it). `secretsEnv` keeps calling
   the declaration-scoped, liveness-gated vault RPCs; additionally, for each
   **declared** name the DAG marks external-sourced, it calls `resolver.Resolve`.
   Precedence: an external hit overrides the vault entry for that name; the vault
   is the fallback. The resolved value is merged into the existing
   `AIRFLOW_VAR_*`/`AIRFLOW_CONN_*` export, so **all task types** (operator,
   python, bash) are covered uniformly.

4. **The declared set is the request filter (ADR 0055) — but it must first be
   threaded to the agent.** The resolver is only ever asked for names the DAG
   declared, which is the set `enforce` scopes the vault to. **New wiring
   required (not pre-existing):** `DeclaredVariables`/`DeclaredConnections` today
   live only on the **server-side** `agentrpc.TaskSpec` (populated in
   `agent_store.go`, consumed server-side for enforce scoping in `secrets.go`);
   the **agent-facing** proto `agentv1.TaskSpec` (`proto/agent.proto`) does not
   carry them, and `secretsEnv` takes no spec. So Layer 1 must add
   `declared_variables`/`declared_connections` (and the per-name external-source
   marking) to `agentv1.TaskSpec`, populate them in `GetTaskSpec`, and thread the
   spec into `secretsEnv`. This is additive and default-off-safe, but it is real
   work, not a free inheritance.

   **Liveness/scope gating is agent-side here, not server-enforced — state the
   trust boundary honestly.** The vault path's declaration-scope + liveness gate
   runs **inside** the RPC handlers `GetVariables`/`GetConnections`
   (`secrets.go`, `checkLiveness` + the enforce SQL filter). The external resolver
   runs in the **agent**, after those RPCs, so it is gated by the agent's own
   choice to **skip external resolution when the vault RPC returns
   `PermissionDenied`** (liveness-enforce denial), backstopped by the pod's cloud
   IAM ceiling (ADR 0048 — the pod is the boundary). It is therefore *consistent
   with* the vault path, not *identically server-enforced*. `GetTaskSpec` is not
   liveness-gated, so the agent always holds the config; the true backstop against
   a buggy/compromised agent resolving for a non-live TI is pod IAM. Under
   `secret_liveness_mode: enforce` a non-live TI resolves nothing (vault RPC
   denied → external skipped); under the default `observe`, a non-live TI still
   receives vault secrets, and external resolves consistently with that (see B2).

5. **Keyless via the pod KSA** (IRSA / EKS Pod Identity for AWS; GKE WI / Azure WI
   / Vault k8s-auth for the rest). The adapter holds no credential; the provider's
   webhook injects its projected token + `AWS_*` env **at admission, after
   `buildPod` runs** — invisible in the executor code, disjoint from leoflow's
   agent token (see B3). leoflow itself sets no `AWS_*` (`podEnv` sets only
   `LEOFLOW_*`; note `execution.env` is an operator passthrough that could carry
   `AWS_*`, operator-controlled, not author). leoflow stores no key (ADR 0035).

6. **No cache in v1** (per-attempt read is cheap and fresh); if ever added,
   **per-pod only**, never process-global (warm-worker caution, B4). **Fail-closed**
   on a required-name miss through the whole chain, and on **any** hard resolver
   error regardless of required-ness (B6). Best-effort skip is retained only for
   **optional** names.

7. **Authoring surface — operator owns the backend, author only references it.**
   Backend **definitions** (which backends exist, their `connections_prefix` /
   `variables_prefix` / region / adapter, and the **coverage predicate** D6
   consults) are **operator/Helm-supplied**, never author-settable. The DAG's
   authoring surface may at most *reference* an operator-defined backend by name /
   mark a declared name as external-sourced — it never defines its own
   `backend_kwargs` that D6 or the resolver trusts. Any `secrets_backend`-shaped
   field that appears in `leoflow-yaml-schema.json` is a reference/marker,
   compiled into `dag.json`, **structural-validate-only** in the Go compiler
   (never resolve at compile — ADR 0048/0035); it must **not** feed the D6
   coverage predicate (see the D6 amendment). This closes the author-defeats-D6
   footgun: if the coverage predicate derived from author `backend_kwargs`, an
   author could declare any name "covered by a backend" and turn D6 into a no-op.

8. **Default adapter is a supply-chain call (ADR 0014), recorded here:** **2b**
   (agent drives the in-pod real-Airflow backend for declared names — zero new Go
   SDK, reuses the ADR 0036/0040 shim) is preferred as the default; **2a** (Go
   SDK in the agent) is available where no in-pod backend exists (Lite) or for a
   provider Airflow lacks. For AWS specifically the 2a delta is small — the AWS
   SDK v2 (`config`, `credentials`, `sts`) is **already vendored server-side** by
   the ADR 0056 log sink; only `secretsmanager`/`ssm` clients are new, and only if
   linked into the agent.

### The one amendment to an existing invariant — ADR 0055 D6 (blocker)

`validateDeclaredSecrets` (ADR 0055 D6) **rejects registration** if a declared
`conn_id`/`var` is not present in the tenant's variables/connections tables. An
externally-sourced secret lives only in the provider — so a DAG that declares
`connections: [databricks]` and sources `databricks` from Secrets Manager would
be **rejected at registration today**. The naive fix — have registration confirm
the name exists in the provider — is **exactly the control-plane egress ADR 0048
forbids**.

**Decision:** registration existence stays **structural/local only**. Relax
`validateDeclaredSecrets` so a declared name **covered by a configured external
backend** is accepted **without any provider call**. Provider existence is proven
only pod-side at resolve time, fail-closed on miss (D6/B6).

**The coverage predicate MUST derive from operator/Helm-supplied backend config,
never from author input.** If the "covered by a backend" test read the DAG's own
`backend_kwargs`, an author could mark any declared name as covered and turn D6
from an existence check into an author-controlled no-op — defeating the whole ADR
0055 D6 protection. The predicate reads only operator config (the Helm-configured
backend prefixes/namespaces). This is the single code change to an existing
invariant and is stated explicitly.

### ADR 0055 cross-note (definitional, low blast radius)

A declared `conn_id`/`var` may be **sourced from an external backend** rather than
the vault; the **declaration is still the scope authority**, and
`secret_scoping: enforce` + token liveness still gate what the agent resolves.
This does not change 0055's mechanism (declaration → scoped, gated request), only
its *sources*.

## Consequences

- **Single source of truth for IaC-provisioned secrets** (#811's ask) without
  abandoning the vault for secrets that only live in leoflow — one resolution
  locus (the agent), one chain, low added surface.
- **ADR 0048 stays intact** — nothing author-influenced runs in core; no ADR 0048
  amendment. **ADR 0055 is extended, not broken** — one definitional cross-note +
  the D6 relaxation. **ADR 0035 is reinforced** — no key stored; the deferred
  `key_secret_name` idea (0035 §2) is realized generically. **ADR 0021's** stated
  evolution ("cloud Workload Identity", "K8s Secret projection") is realized:
  Layer 1 ships the first, Layer 0 the second.
- **All task types covered** — the resolver feeds the env-export path, so bash and
  `os.environ` tasks see externally-sourced secrets, which a pure in-pod Airflow
  backend would miss.
- **Multi-provider by design** — AWS is the reference adapter; GCP/Azure/Vault are
  additional adapters behind the same port, differing only in path convention,
  value decoding, and which keyless mechanism the SDK default chain invokes.
- **Costs.** Effort **M** (port + AWS adapter + agent wiring + config) on top of
  the already-shipped Layer 0 docs. Ships **default-off**: with no
  `secrets_backend` configured, behavior is byte-identical to today, so k3d/kindnet
  CI is unchanged. The keyless end-to-end path is **NEEDS-REAL-CLUSTER** and is
  proven on an EKS RC, never in CI.
- **A new failure mode, intended:** a declared-required name absent in both the
  provider and the vault now **fails the task** (was a silent skip). This composes
  with #798 required-params.

## Alternatives considered

| Model | Effort | ADR 0048 | ADR 0035 | ADR 0055 scoping | Non-operator tasks | Verdict |
|---|---|---|---|---|---|---|
| 1 — pod-side native Airflow backend (operator sets `AIRFLOW__SECRETS__BACKEND` on the pod) | S | clean | clean | **lost → coarse cloud IAM** | **not covered** | partial; foundation for 2b |
| 2a — Go SDK resolver in the agent | L | clean (pod-side) | Go SDK surface (ADR 0014) | preserved | covered | strong but heavy; keep for Lite/provider gaps |
| **2b — agent drives in-pod Airflow backend for declared names** | **M** | **clean** | **clean** | **preserved** | **covered** | **chosen** |
| 2 (control-plane resolution) | — | **REJECTED** | — | — | — | ADR 0048 forecloses it |
| 3 — ESO/CSI, docs-only | XS | clean | clean | out-of-band (K8s RBAC) | covered (env/file) | **Layer 0 — ship as escape hatch (done)** |

- **Model 1 alone** is cheapest but silently drops ADR 0055 declaration-scoping
  onto coarse cloud IAM and under-covers bash/`os.environ` tasks. 2b *is* Model 1
  driven by the agent for declared names — it keeps both properties for a modest
  increment.
- **Control-plane resolution** (pre-populate the vault from an external store at
  registration) is rejected on sight by ADR 0048; it would need a high-blast-radius
  0048 carve-out and still lets an author's declared name drive which secret core
  reads. Not recommended.

## Go / no-go

**GO to build the AWS slice** (port + AWS reference adapter + agent wiring +
config, **default-off**), conditioned on three non-negotiable gates from the
security review:

- **B1 (blocker):** the ADR 0055 D6 relaxation lands first or in the same PR — else
  external-sourced DAGs cannot register, and the "obvious" fix breaches ADR 0048.
- **B2 (blocker):** the external branch is gated **agent-side on the vault RPC
  outcome** — a `PermissionDenied` (liveness-enforce denial) from
  `GetVariables`/`GetConnections` must **skip** external resolution, not fall
  through the current best-effort skip — backstopped by pod IAM. It is consistent
  with the vault path, not server-enforced like it; state that boundary honestly.
- **B6 (major, GA-gate if #798 ships):** fail closed on a required-name miss or any
  hard resolver error; best-effort skip only for optional names.

Recorded (not gates): **B5** — 2b-vs-2a supply-chain decision (default 2b);
**B4** — no cache in v1, per-pod only if ever added.

## What a real cluster must prove (EKS RC — CI never will)

k3d + kindnet models neither IRSA/Pod-Identity token flows, real STS, nor
NetworkPolicy enforcement. The following are **NEEDS-REAL-CLUSTER** and gate the
RC pass, not the code:

1. A task pod running as a KSA annotated for IRSA (or a Pod Identity association)
   resolves a Secrets Manager secret via the SDK default chain with **no static
   creds anywhere** — assert the resolved `AIRFLOW_CONN_*` is present/correct and
   the pod spec / etcd hold no credential.
2. **Three-token coexistence:** the `leoflow-control-plane` token exchanges for the
   leoflow JWT **and** the `sts.amazonaws.com` token drives STS, with
   `automountServiceAccountToken:false` and no default token mounted. (Code posture
   is SOUND now — automount-false disables only the default token; IRSA/Pod
   Identity inject their own disjoint projected tokens at admission.)
3. **NetworkPolicy egress:** with a task-pod default-deny-egress policy, confirm the
   allowlist reaches STS + Secrets Manager (IRSA) or `169.254.170.23:80` (Pod
   Identity), measured per CNI (AWS VPC CNI vs Calico). On **AWS VPC CNI**, confirm
   the network-policy agent is actually enabled (off by default on older clusters)
   before asserting egress-allowlist behavior; on **Calico** confirm separately —
   they enforce differently and kindnet enforces nothing. Also assert the IRSA
   webhook's injected volume **coexists** with leoflow's projected
   `leoflow-agent-token` volume: both mount, `automountServiceAccountToken:false`
   survives admission, and STS `AssumeRoleWithWebIdentity` succeeds while the agent
   still exchanges its `leoflow-control-plane` token.
4. **Fail-closed:** a declared-required name absent in both the provider and the
   vault fails the task legibly; an AccessDenied fails closed.
5. **Cross-tenant isolation** (meaningful once warm workers exist): two tenants'
   tasks with different KSAs resolve only their own secrets.

## Revisit triggers

- The first **non-AWS adapter** — re-check port neutrality against the GCP/Azure/
  Vault value-shape and keyless mechanics.
- The **warm-worker regime** lands (ADR 0058) — re-check the cache/cross-tenant
  caution (B4): any cache must key on `(tenant, name)` and be attempt-scoped.
- Any request for **control-plane pre-population** of the vault from an external
  store — that needs an ADR 0048 amendment (high blast radius; currently not
  recommended).

## Verify at implementation

- Default-off produces byte-identical pod specs and env to today (golden test).
- **Required:** an import/build guard test proving the resolver package is
  unreachable from `cmd/leoflow-server` and from the compiler / registration path
  (structural-only) — the mechanical proof of the ADR 0048 boundary, not optional.
- `validateDeclaredSecrets` accepts an externally-backed declared name with **no**
  provider call, and the coverage predicate reads **operator config only** — a
  DAG-supplied `backend_kwargs` cannot make an arbitrary name "covered" (unit test
  asserting no network + author-input rejection).
- The declared set is threaded onto the agent-facing `agentv1.TaskSpec` and
  `secretsEnv` asks the resolver only for declared names (proto + wiring test).
- The external branch is skipped when the vault RPC returns `PermissionDenied`
  (liveness-enforce), not merely absent — a non-live TI (under enforce) resolves
  nothing externally (test the skip-on-`PermissionDenied` path, distinct from the
  best-effort skip).
- A required declared name missing through the whole chain fails the task; an
  optional one is skipped; a hard resolver error fails closed (table test).
