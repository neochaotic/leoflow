---
title: External secrets (keyless, ESO, and mounted secrets)
linkTitle: External secrets
weight: 58
description: Reach credentials that live in your cloud secret store or Vault from task pods — keyless first, then External Secrets Operator or a mounted Kubernetes Secret — without duplicating them in Leoflow.
---

Leoflow is **not a key manager** ([ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)). A
Connection or Variable does not have to live in Leoflow's vault: if your secret
already exists in **AWS Secrets Manager, GCP Secret Manager, Azure Key Vault, or
HashiCorp Vault** — provisioned by Terraform, synced by the External Secrets
Operator, etc. — a task can reach it **without a copy in Leoflow**. That keeps a
single source of truth and stays fully declarative/IaC.

There are three ways for a task to reach an external secret, **in order of
preference**. Prefer the earliest one your environment allows.

{{% alert title="Security: less secret material is safer" color="warning" %}}
Every copy of a credential is a place it can leak. Keyless (option 1) keeps
**zero** secret material anywhere — no value in Leoflow, no Kubernetes Secret, no
env var. Reach for options 2–3 only when a credential (not a cloud identity) is
genuinely required.
{{% /alert %}}

## 1. Keyless — Workload Identity (preferred)

The task pod runs as a Kubernetes ServiceAccount bound to a cloud identity, and
the cloud SDK inside the task uses **that identity** — no key, no token, nothing
to rotate or leak. This is the recommended path for any cloud connection.

| Cloud | Mechanism |
|---|---|
| AWS | **IRSA** or **EKS Pod Identity** — the pod's SA assumes an IAM role |
| GCP | **Workload Identity** — the KSA impersonates a Google service account |
| Azure | **Azure Workload Identity** — the KSA federates to a managed identity |
| HashiCorp Vault | **Kubernetes auth** — Vault trusts the pod's SA token |

AWS, GCP, and Azure each have a native, keyless-first Leoflow Connection type
(`aws`, `google_cloud_platform`, `wasb`/`adls`/…) that resolves this identity
automatically — see [ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/).
Vault has no native Leoflow Connection type: a task reaches it by using Vault's
own client library with the pod's ambient ServiceAccount token, independent of
Leoflow's Connection model.

The Connection then declares **no key at all** — e.g. a `google_cloud_platform`
connection with neither `key_path` nor `keyfile_dict` resolves via Application
Default Credentials (the pod identity). Set up the identity binding via the
chart's task ServiceAccount, then point a task at it:

```yaml
# values.yaml
taskServiceAccount:
  create: true
  name: leoflow-task
  annotations:
    # GKE Workload Identity:
    iam.gke.io/gcp-service-account: "<GSA>@<project>.iam.gserviceaccount.com"
    # EKS IRSA (use this OR the GKE annotation, not both):
    # eks.amazonaws.com/role-arn: "arn:aws:iam::<account>:role/<role>"
```

```yaml
# leoflow.yaml — reference that ServiceAccount from a task
tasks:
  my_task_id:
    execution:
      service_account: leoflow-task
```

Leoflow passes the pod identity through untouched; it never sees a token or key.

## 2. External Secrets Operator (ESO) → a mounted Kubernetes Secret

When a **credential file** is genuinely required (a service-account JSON, a
client certificate, a private CA) and keyless is not available, keep the secret
in your external store and let **[ESO](https://external-secrets.io/)** sync it
into a Kubernetes Secret. Leoflow mounts that Secret read-only into task pods;
the Connection references the file by path. **The secret value never enters
Leoflow** — Leoflow only mounts a Secret you (or ESO) created.

This is available **today**, provider-neutral (ESO supports AWS/GCP/Azure/Vault
and more), and needs no Leoflow code.

1. **ESO syncs the external secret into a Kubernetes Secret** (illustrative — AWS
   Secrets Manager; the same shape works for any ESO provider):

   ```yaml
   apiVersion: external-secrets.io/v1beta1
   kind: ExternalSecret
   metadata:
     name: gcp-sa-key
     namespace: leoflow           # the chart's taskNamespace (default: leoflow)
   spec:
     refreshInterval: 1h
     secretStoreRef:
       name: aws-secrets-manager  # your ClusterSecretStore/SecretStore
       kind: ClusterSecretStore
     target:
       name: gcp-sa-key           # the Kubernetes Secret ESO creates
     data:
       - secretKey: key.json      # the file name inside the Secret
         remoteRef:
           key: prod/gcp/etl-sa-key  # the secret's id in the external store
   ```

2. **Mount that Kubernetes Secret into task pods** via the chart's `taskSecret`
   (the chart only mounts it — it never reads or copies the value):

   ```yaml
   # values.yaml
   taskSecret:
     name: gcp-sa-key                   # the Secret from step 1
     mountPath: /etc/leoflow/secrets    # read-only mount in every task pod
   ```

3. **Reference the mounted file from the Connection** with `key_path`
   ([ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)) — the key
   is read from disk at task time, never stored in Leoflow. Create or edit the
   Connection via **Admin → Connections** or the API, setting `key_path` in
   Extra:

   ```bash
   curl -X POST "$LEOFLOW_SERVER/api/v2/connections" -H "Authorization: Bearer $TOKEN" \
     -H 'Content-Type: application/json' \
     -d '{"connection_id":"warehouse","conn_type":"google_cloud_platform","extra":"{\"key_path\":\"/etc/leoflow/secrets/key.json\"}"}'
   ```

   Then declare it so a DAG's tasks can consume it:

   ```yaml
   # leoflow.yaml
   connections:
     - warehouse
   ```

See the per-connection pages (e.g.
[Google Cloud Platform](/connections/google_cloud_platform/)) for the exact
`extra` fields each connection type accepts.

{{% alert title="CSI Secret Store driver" color="info" %}}
The [Secrets Store CSI Driver](https://secrets-store-csi-driver.sigs.k8s.io/)
(with the AWS/GCP/Azure/Vault providers) is an equivalent mount-based
alternative: it mounts the external secret straight into the pod as a file,
which a Connection then references by `key_path` exactly as above. Use whichever
your platform already runs.
{{% /alert %}}

## 3. A hand-created mounted Kubernetes Secret

Without ESO or CSI, create the Kubernetes Secret yourself
(`kubectl create secret generic gcp-sa-key --from-file=key.json=...`) and mount
it with the same `taskSecret` config as step 2. Identical from Leoflow's side —
you just own the sync instead of ESO.

## What this does and does not cover

- **File-based credentials** (SA keys, certs, CA bundles) are the natural fit for
  options 2–3 via `key_path`.
- **Cloud API access** (BigQuery, S3, a warehouse reached by role) is best served
  by **option 1 (keyless)** — no secret at all.
- Some connection types already have a narrower, provider-specific direct-fetch
  field — e.g. `google_cloud_platform`'s `key_secret_name` fetches a key straight
  from **GCP Secret Manager** via ADC at task time (still needs a keyless
  identity to *read* the secret). That's a per-connection escape hatch, not the
  general mechanism this page covers.
- **Resolving an arbitrary Connection/Variable directly from the external store**
  — so a token-style secret in AWS Secrets Manager becomes a Leoflow Connection
  with no Kubernetes Secret in between — is **not yet** built. It's tracked as an
  open feature request ([#811](https://github.com/neochaotic/leoflow/issues/811))
  with an advisory design study, but there's no accepted ADR or shipped code yet.
  Until it lands, use keyless (option 1) for token-style access, or sync via ESO
  (option 2).

## Scoping — which pod sees which secret

How a credential is isolated to the right task depends on the path it takes:

- **Mounted Kubernetes Secret (options 2–3) is cluster-wide, not per-task.** The
  `taskSecret` mount is applied to **every** task pod, so any task can read the
  files under `mountPath`. Isolate it *outside* Leoflow: put only broadly-shared
  material in that Secret, separate sensitive workloads by namespace/cluster, and
  restrict who can read the Secret with RBAC. Better still, use **keyless
  (option 1)** — there is no mounted material to over-share.
- **Keyless (option 1) is scoped by the pod's own identity.** A task reaches a
  cloud API as the ServiceAccount identity you bound to its pod; another task with
  a different ServiceAccount cannot assume it. No secret is delivered at all.
- **Leoflow-vault Connections/Variables are scoped per-task by declaration.** For
  secrets stored in Leoflow (not the subject of this page), the control plane
  hands a task pod **only the names that task declared**, delivered against a
  short-lived identity bound to that specific task attempt and only while the
  attempt is live — a pod cannot request another task's secrets. See
  [Variables & Connections](/author-dags/variables-connections/) and
  [ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/). The roadmap
  resolver ([#811](https://github.com/neochaotic/leoflow/issues/811)) brings this
  same per-task scoping to *external* secrets — the gap the cluster-wide mount
  leaves today.

## Security notes

- The `taskSecret` mount is **read-only** and applies to **every** task pod;
  scope the Kubernetes Secret's contents accordingly (see Scoping above).
- **Prefer keyless.** A mounted key is a credential at rest in the cluster;
  Workload Identity is not.
- **Rotation** is your external store's job — ESO re-syncs on its
  `refreshInterval`, and each task runs in a fresh pod that re-reads the mount, so
  there is no long-lived cached copy to invalidate.
- Leoflow never logs or persists a mounted secret's value; it only sets the mount
  path on the pod.

## See also

- [ADR 0035 — Cloud connector auth: keyless-first](/project/adrs/0035-cloud-connector-auth-keyless-first/)
- [Variables & Connections](/author-dags/variables-connections/) — how secrets reach a task.
- [Connections reference](/connections/) — per-type `extra` fields (`key_path`, …).
- [Helm chart](/operate/helm-chart/) — the `taskSecret` values.
