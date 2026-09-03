---
title: Control-plane HA and disruption posture
linkTitle: Control-plane HA
weight: 75
description: "Why a single control-plane replica is evicted as routine housekeeping, what each restart costs, and the one-switch HA profile that turns it into seconds of failover."
---

A Pro control plane with one replica is not "stable until something breaks". It
is evicted as **routine cluster housekeeping** — and every eviction is a full
restart. This page explains the restart window and what is at risk inside it,
why running two replicas is the recommended production posture, the storage
precondition that makes it safe, and how the chart guards the combinations that
are not.

## The restart window

When the single control-plane pod is evicted, the replacement has to:

1. **Pull the image** on whatever node it lands on (nothing cached — the old node
   is usually the one being drained).
2. **Boot**: connect to Postgres and Redis, start the API, gRPC and metrics
   listeners, pass readiness.
3. **Take leadership**: acquire the scheduler's advisory lock
   ([ADR 0009](/project/adrs/0009-leader-election/)) and start the loop. For a
   post-leadership grace window the infra reapers (agent-lost, pod-lost) hold
   their destructive verdicts, so a task that merely lost its control plane for
   the duration of the restart is not falsely reaped
   ([scheduler resilience](/operate/scheduler-resilience/)).

Measured on a production drill (EKS Auto Mode), that window is **48–80 seconds**,
dominated by the image pull. During it:

- **Nothing dispatches.** Queued task instances wait; scheduled runs slip.
- **In-flight tasks lose their control plane.** Agents keep running the task and
  retry their heartbeats and outcome reports, but a long enough outage pushes
  them toward the agent-lost threshold. The scheduler's post-restart grace and
  backoff exist precisely so a restart does not turn healthy running tasks into
  false `agent_lost` failures — but they bound the damage, they do not remove
  the window.
- **The API and UI are down.** Every replica is the same binary, so one replica
  means one API.

## Why eviction is routine, not exceptional

On a managed autoscaling platform the control-plane pod is just another pod to
bin-pack:

- **Karpenter (EKS Auto Mode, or self-managed)** consolidates under-utilized
  nodes as a matter of course: it evicts the pods, deletes the node, and the pods
  reschedule elsewhere. A single-replica control plane is consolidated like any
  stateless workload — the drill above observed it as ordinary bin-packing, not
  a failure.
- **GKE Autopilot** does the equivalent — the platform owns the nodes and
  compacts them.
- **Node auto-upgrades** (EKS, GKE, AKS) drain every node in turn on a schedule
  you may not control.
- **Involuntary disruptions** — node failure, spot/preemptible reclamation, a
  kernel panic, a kubelet crash — hit every platform and give no warning that a
  budget could honor.

The first three are *voluntary* disruptions: Kubernetes asks before evicting and
honors a PodDisruptionBudget. The last is not. The posture below handles both.

## HA is the recommended production posture

The control plane already supports more than one replica: the scheduler
**leader-elects** — one active scheduler, the others standing by on the same
Postgres advisory lock — and the API serves **active-active** from every replica
([ADR 0009](/project/adrs/0009-leader-election/); the api/scheduler role split of
[ADR 0049](/project/adrs/0049-split-api-and-scheduler-roles/) builds on the same
mechanism). With `replicaCount: 2`:

- an eviction of the leader becomes a **failover measured in seconds** — the
  standby is already pulled, booted and connected; it only has to win the lock
  (followers poll every few seconds);
- the API and UI stay up on the surviving replica;
- a rolling upgrade is a real rolling upgrade instead of a stop-the-world
  `Recreate`.

### One switch: the HA profile

The chart ships a complete overlay,
[`helm/leoflow/examples/values-ha.yaml`](https://github.com/neochaotic/leoflow/blob/main/helm/leoflow/examples/values-ha.yaml):

```bash
helm upgrade --install leoflow oci://ghcr.io/neochaotic/charts/leoflow --version <x.y.z> \
  -n leoflow -f values-ha.yaml
```

It sets, and documents why:

| Value | HA profile | Purpose |
|---|---|---|
| `replicaCount` | `2` | leader-elected scheduler standby + active-active API |
| `logs.persistence.enabled` / `logs.sink.provider` | `false` / `s3` (or `gcs`) | task logs in object storage — the recommended HA log path (see below) |
| `podDisruptionBudget.enabled` | *unset (auto)* | the PDB renders itself because the replica floor is above one |
| `terminationGracePeriodSeconds` | `60` | the SIGTERM drain + lease release completes before SIGKILL |
| `podAnnotations` → `karpenter.sh/do-not-disrupt` | *commented out* | EKS/Karpenter-only opt-in, see below |

Edit the `CHANGEME` datastore URLs, secrets and bucket, and annotate the
control-plane ServiceAccount with the cloud identity that may write the bucket
(IRSA on EKS, Workload Identity on GKE).

{{% alert title="Why the chart does not default to two replicas" color="info" %}}
Because it would break every existing install on upgrade. The default install
keeps task logs on a `ReadWriteOnce` PVC; a second replica scheduled to another
node hangs in `ContainerCreating` on a **Multi-Attach** error while `helm
upgrade` reports success. "First-class HA" therefore means an explicit,
documented, one-file profile plus render-time guards — not a flipped default.
{{% /alert %}}

## The storage precondition

Task pods stream their logs over gRPC to the control plane, which persists them
under `config.logsDir`. Every replica must be able to **write** (the leader
does) and **read** (any API replica may serve the log) the same store. That
rules out the default volume:

| Log store | HA-safe? | Notes |
|---|---|---|
| **Object storage** — `logs.persistence.enabled: false` + `logs.sink.provider: s3` or `gcs` ([ADR 0056](/project/adrs/0056-task-log-object-sink/)) | **Yes — recommended** | no PVC at all; the bucket owns durability and retention; keyless auth via the control-plane ServiceAccount; the Deployment can roll |
| **`ReadWriteMany` PVC** — `accessMode: ReadWriteMany` on an RWX class (EFS, GCP Filestore, Azure Files, NFS, CephFS, Longhorn-rwx) | Yes | the alternative when no object store is reachable from the control plane; needs an RWX-capable StorageClass |
| **`ReadWriteOnce` / `ReadWriteOncePod` PVC** (the default) | **No** | attaches to one node / one pod; a second replica Multi-Attach-deadlocks |
| **emptyDir** — `logs.persistence.enabled: false` with `logs.sink.provider: disk` | No | each replica keeps its own logs: the leader's logs are invisible to the other replica's API, and everything is lost on restart. Dev only; the chart's NOTES warn if you do this at `replicaCount > 1` |

The chart refuses to render the unsafe combination. `replicaCount > 1` (or an
HPA with `maxReplicas > 1`, or the api/scheduler split — any shape where more than
one pod mounts the PVC) with a single-writer access mode fails `helm install` /
`helm upgrade` / `helm template` with:

```text
more than one control-plane pod would mount the logs PVC (replicaCount > 1,
autoscaling.maxReplicas > 1, or split.enabled), but
logs.persistence.accessMode=ReadWriteOnce attaches the volume to a single
node/pod: the extra pods hang in ContainerCreating with a Multi-Attach error
while the install reports success. HA needs a log store every replica can write.
Recommended: set logs.persistence.enabled=false and ship task logs to object
storage (logs.sink.provider=s3|gcs with logs.sink.bucket). Alternative:
logs.persistence.accessMode=ReadWriteMany on an RWX StorageClass (EFS, Filestore,
Azure Files, NFS, CephFS, Longhorn-rwx). Or keep a single replica. See
helm/leoflow/examples/values-ha.yaml.
```

Safe-by-default means exactly this: HA can never silently deploy onto a volume
only one pod can hold.

### Moving an existing single-replica install to HA

1. **Pick the log store first.** Switch to the object sink (recommended) or
   re-create the logs PVC on an RWX class. The chart cannot convert an existing
   `ReadWriteOnce` claim in place — access modes are immutable on a bound PVC.
2. **Know what happens to old logs.** Logs written before the switch stay where
   they were. With the object sink the control plane serves logs from the bucket
   only, so keep the old PVC around (or copy it into the bucket under the same
   `{prefix}/{tenant}/{dag}/{run}/{task}/{try}.log` layout) if you need the
   history in the UI.
3. **Apply the profile.** `helm upgrade -f values-ha.yaml`. With the PVC gone the
   update strategy auto-selects `RollingUpdate`, the second replica comes up
   alongside the first, and the PDB appears.
4. **Confirm leadership moves.** Delete the leader pod once and watch the
   standby take the lock within seconds — no image pull, and the dispatch gap is
   the re-election poll, not a restart.

## The PodDisruptionBudget — and the single-replica trap

A PodDisruptionBudget tells the eviction API how many pods must stay up during a
*voluntary* disruption. With two replicas and `minAvailable: 1`, a drain or a
consolidation evicts one replica, waits for the other to be ready, then the next
— the control plane never disappears. That is why the chart renders the PDB
**automatically when the guaranteed replica floor is above one** (`replicaCount`,
or the HPA `minReplicas`, or `split.api.replicaCount` in split mode).

The same budget over a **single** replica is a trap. `minAvailable: 1` over one
pod means *no* voluntary eviction of that pod is ever allowed:

- `kubectl drain` hangs on it indefinitely.
- Cluster and node auto-upgrades stall on that node — until the platform's
  patience runs out and it overrides the budget anyway.
- Autoscaler consolidation of that node is blocked (Karpenter) — which sounds
  like protection but leaves an under-utilized node pinned by one pod.
- And none of it helps against an involuntary disruption: the pod still dies
  with the node.

It protects the pod at the cost of cluster operations. So the chart never turns
it on for a single replica by default; `podDisruptionBudget.enabled: true` still
forces it (an informed choice — the install NOTES say what it costs), and
`false` forces it off even in HA.

### How platforms honor a PDB

| Platform | Voluntary disruption source | PDB behavior |
|---|---|---|
| **EKS with Karpenter** (incl. Auto Mode) | consolidation, drift, node expiration | Honored: a node whose pod is blocked by a PDB is not disrupted. A NodePool `terminationGracePeriod` can force the drain past a blocking PDB after that period — check yours. The pod annotation below exempts the node from voluntary disruption entirely |
| **GKE Standard** | cluster autoscaler scale-down, node auto-upgrade, maintenance | Honored on scale-down (the node is not removed). Upgrades and maintenance honor the budget for a **bounded** window (on the order of an hour per node), then proceed |
| **GKE Autopilot** | platform compaction, auto-upgrade | Same as Standard, with the platform owning the nodes: honored, bounded, then overridden |
| **AKS** | cluster / node-image upgrade, node pool scale-down | Honored during drain; when the drain cannot complete within the timeout the **upgrade operation fails** rather than overriding the budget, so a blocking PDB fails your maintenance instead of protecting you through it |
| Any — `kubectl drain`, cluster-autoscaler | manual drain, scale-down | Honored; a blocked single replica hangs the drain until someone adds `--disable-eviction` or deletes the pod |

Two replicas plus the auto PDB behave well on **every** row of that table. A
single replica with a forced PDB behaves badly on every row.

## `karpenter.sh/do-not-disrupt` — EKS/Karpenter-only opt-in

Karpenter honors a pod annotation, `karpenter.sh/do-not-disrupt: "true"`, that
exempts the pod's **node** from Karpenter's voluntary disruption: no
consolidation, no drift replacement, no expiration while the pod runs. The chart
wires `podAnnotations` into the control-plane pod template, so it is one
uncommented line in the HA profile:

```yaml
podAnnotations:
  karpenter.sh/do-not-disrupt: "true"
```

It is deliberately **not** set by default:

- it is meaningless outside Karpenter (GKE, AKS and a plain cluster-autoscaler
  ignore it);
- it trades **maintenance friction for eviction protection** — that node is never
  consolidated or rolled by Karpenter while the control plane sits on it, so
  drift (a new AMI) and consolidation savings stop at that node;
- it does nothing against involuntary disruptions.

Reach for it only when you have measured that consolidation on your cluster
still evicts the control plane faster than failover recovers it — and prefer
the second replica first.

## Drain grace

A voluntary eviction sends `SIGTERM` and waits `terminationGracePeriodSeconds`
(Kubernetes default 30s) before `SIGKILL`. On `SIGTERM` the server drains its
HTTP listeners and releases the scheduler lock so the standby can take it at
once. If the grace expires first, the leader is killed mid-step-down and the
follower must wait for Postgres to notice the dead session instead — slower and
noisier than a clean handoff. The HA profile sets the value to `60`; the chart
omits the field when unset so a default install's pod spec is byte-for-byte
unchanged.

## Involuntary disruptions: why HA is the posture that matters

No PDB, annotation or grace period prevents:

- **node failure** — hardware, kernel panic, kubelet or container-runtime crash;
- **spot / preemptible reclamation** — a two-minute notice at best, no eviction
  API involved;
- **zone or network partition** that isolates the node;
- **platform-side forced maintenance** once a bounded PDB window expires.

Each of those is the 48–80 second restart window with no warning. The only
thing that shrinks it is a replica that is already running somewhere else — and
the scheduler's own resilience (the post-leadership grace on the infra reapers,
at-most-once reaping, and the reconciler that recovers a pod's durable outcome,
described in [scheduler resilience](/operate/scheduler-resilience/)) is what keeps the tasks
that were in flight during the window from being falsely failed. HA shortens the
window; the resilience mechanisms make the remaining window survivable. Run both.

## Related

- [Helm chart](/operate/helm-chart/) — the entry point and the chart README
  with the full values table.
- [Scheduler resilience](/operate/scheduler-resilience/) — what happens to
  in-flight tasks around a restart.
- [Upgrades](/operate/upgrades/) — rolling a control-plane release, edition by
  edition.
- [ADR 0009](/project/adrs/0009-leader-election/) — leader election;
  [ADR 0049](/project/adrs/0049-split-api-and-scheduler-roles/) — api/scheduler
  split; [ADR 0056](/project/adrs/0056-task-log-object-sink/) — the task-log
  object sink.
