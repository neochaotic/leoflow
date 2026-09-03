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
   ([ADR 0009](/project/adrs/0009-leader-election/)) and start the loop. For the
   **post-leadership grace** (180 s by default) the two reapers that judge a
   task by its pod — **agent-lost** and **pod-lost** — hold their verdicts, so a
   task that merely lost its control plane for the duration of the restart is
   not failed before the reconciler has recovered its durable outcome. The other
   reapers (dispatch-lost, orphan-run, warm-worker-lost) carry no such grace
   ([scheduler resilience](/operate/scheduler-resilience/)).

Measured on a production drill (EKS Auto Mode), that window is **48–80 seconds**,
dominated by the image pull. During it:

- **Nothing dispatches.** Queued task instances wait; scheduled runs slip.
- **In-flight tasks lose their control plane.** Agents keep running the task and
  retry their heartbeats and outcome reports until the server returns. The
  post-leadership grace on agent-lost and pod-lost is what keeps that from
  becoming a false `agent_lost` / `pod_lost` failure; the server also validates
  at boot that the timing ladder this depends on holds (heartbeat < agent-lost
  threshold < grace < token TTL, and the reconcile interval below both graces),
  refusing to start if a knob was moved out of order. These bound the damage;
  they do not remove the window.
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
([ADR 0009](/project/adrs/0009-leader-election/)). With `replicaCount: 2`
(non-split — see [split mode](#split-mode-is-not-dispatch-ha) below):

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
| `topologySpreadConstraints` | hostname spread, `ScheduleAnyway` | two replicas bin-packed onto one node are one replica (see below) |
| `logs.persistence.enabled` / `logs.sink.provider` | `false` / `s3` (or `gcs`) | task logs in object storage — the recommended HA log path (see below) |
| `resources` | `1Gi` request / `2Gi` limit | the object sink keeps each running attempt's log in memory until the attempt ends (it is flushed incrementally, but re-uploaded whole); size for your fan-out |
| `podDisruptionBudget.enabled` | *unset (auto)* | the PDB renders itself because the replica floor is above one; `unhealthyPodEvictionPolicy: AlwaysAllow` |
| `terminationGracePeriodSeconds` | `60` | headroom for the HTTP shutdown, the dispatch-pool drain and the bounded gRPC stop (see below) |
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

### Two replicas on one node are one replica

Nothing in Kubernetes keeps two replicas of a Deployment apart by default, and
autoscaler consolidation actively pushes them together — bin-packing both onto
one node is exactly what it is for. Then a node failure takes the whole control
plane, and the auto PDB makes it worse: `minAvailable: 1` with both pods on the
node being drained **blocks the drain**, which is the single-replica trap one
level up. The profile therefore sets a `topologySpreadConstraints` entry on
`kubernetes.io/hostname` with `maxSkew: 1`:

```yaml
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: ScheduleAnyway
```

`whenUnsatisfiable: ScheduleAnyway`, deliberately not `DoNotSchedule`: on a
single-node cluster `DoNotSchedule` leaves the second replica `Pending` forever,
and the PDB then blocks every drain of the one node. A constraint that omits
`labelSelector` gets the Deployment's own selector labels from the chart, so the
values file does not need to know the release name. On multi-zone clusters add
a second constraint on `topology.kubernetes.io/zone` (also `ScheduleAnyway`).

### Split mode is not dispatch-HA

The api/scheduler role split
([ADR 0049](/project/adrs/0049-split-api-and-scheduler-roles/)) is a
**security** topology, not an availability one. Its `api` Deployment is HA — it
runs `split.api.replicaCount` active-active replicas and gets the auto PDB — but
its `scheduler` Deployment is **pinned to one replica** by the chart, with no
standby and, correctly, no PDB (a budget over one pod would block every drain).
Every eviction of the split scheduler is therefore still the full restart window
above: image pull, boot, leadership. Non-split `replicaCount: 2` is the only
topology that gives dispatch failover today; choose the split when you need the
restricted API identity more than scheduler failover, and know which one you
picked.

## The storage precondition

Task pods stream their logs over gRPC to the control plane, which persists them
under `config.logsDir`. Every replica must be able to **write** (the leader
does) and **read** (any API replica may serve the log) the same store. That
rules out the default volume:

| Log store | HA-safe? | Notes |
|---|---|---|
| **Object storage** — `logs.persistence.enabled: false` + `logs.sink.provider: s3` or `gcs` ([ADR 0056](/project/adrs/0056-task-log-object-sink/)) | **Yes — recommended** | no PVC at all; every replica reads the same bucket; retention is the bucket's lifecycle policy; keyless auth via the control-plane ServiceAccount; the Deployment can roll. Read the durability note below |
| **`ReadWriteMany` PVC** — `accessMode: ReadWriteMany` on an RWX class (EFS, GCP Filestore, Azure Files, NFS, CephFS, Longhorn-rwx) | Yes | the alternative when no object store is reachable from the control plane; the disk sink flushes through a 1 MiB buffer, so a mid-attempt control-plane death loses at most that last chunk; needs an RWX-capable StorageClass |
| **`ReadWriteOnce` / `ReadWriteOncePod` PVC** (the default) | **No** | attaches to one node / one pod; a second replica Multi-Attach-deadlocks |
| **emptyDir** — `logs.persistence.enabled: false` with `logs.sink.provider: disk` | No | each pod keeps its own logs: the leader's logs are invisible to every other pod's API, and everything is lost on restart. Dev only. The chart's NOTES warn whenever more than one pod can mount it (`replicaCount`, the HPA ceiling), and **refuse to render** it in split mode, where the scheduler is the only writer and no api pod could ever read a log |

**What the object sink makes durable, and when.** The live tail the UI shows
while a task runs is Redis pub/sub and works from any replica. The durable
object is **rewritten incrementally while the attempt runs**: object stores
have no append, so the control plane accumulates the attempt in memory and
re-uploads the whole object (an overwrite `Put` of the same key) whenever the
unflushed tail reaches 1 MiB, or every 5 s while anything is unflushed, with a
final write on close. Once the stored object is large the cadence is damped —
a flush waits until the unflushed tail is at least one eighth of what is
already stored — so a big log costs about nine times its final size in upload
rather than a quadratic bill; small logs (the common case) trail the live tail
by at most a few seconds. What a kill loses is therefore **the unflushed tail
of each running attempt** — for a typical task, the lines of the last ~5 s —
not the attempt's whole log; and an orderly shutdown loses nothing on the
control-plane side, because open log streams are closed and flushed at
`SIGTERM` (see [drain grace](#drain-grace)). Two honest caveats. First, the
agent does not currently re-open a log stream the control plane closed, so the
lines a task prints *after* its control plane went away are not shipped by that
task; the task itself is unaffected, the reconciler still recovers its outcome,
and a replacement control plane serves the stored partial log. Second, a bucket
with **object versioning** keeps every rewrite as a version — set a lifecycle
rule on noncurrent versions or expect the versioned size to be several times
the log size. On memory: the whole attempt is still held in RAM until it ends
(capped at 128 MiB per attempt), so size `resources.limits.memory` for
*concurrent attempts × their log volume*, not for the chart's 512 Mi default (a
wide fan-out of chatty tasks can OOM a small control plane, and OOM is a
restart — the flushed prefix survives it). If even a few seconds of tail is
unacceptable for you, prefer the RWX PVC, which flushes at 1 MiB without
re-uploading.

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
   alongside the first on another node (the spread constraint), and the PDB
   appears — the install NOTES say so.
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

Two more details the chart gets right for you:

- **`unhealthyPodEvictionPolicy: AlwaysAllow`** (the chart default; `""` omits
  the field for apiservers older than 1.27). Kubernetes' own default,
  `IfHealthyBudget`, refuses to evict even *unhealthy* pods once the budget is
  unmet — so with both replicas unready (bad database credentials after a
  rotation, an image-pull failure, a wedged rollout) a node drain hangs on a
  control plane that is already down. `AlwaysAllow` lets the broken pods go.
- **Auto mode counts every shape.** The floor is `replicaCount`, or the HPA
  `minReplicas` when autoscaling is on, or `split.api.replicaCount` in split
  mode. That means an existing `split.enabled` install (api default 2) or
  `autoscaling.enabled` install (`minReplicas` default 2) **gains a PDB on
  upgrade** to this chart version; the upgrade NOTES call it out, and
  `podDisruptionBudget.enabled: false` opts out if your maintenance tooling
  assumed none.

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
(Kubernetes default 30s) before `SIGKILL`. Be precise about what the grace buys,
because it is not leadership:

- **Leadership handoff does not depend on it.** The scheduler releases its
  advisory lock within about one tick of `SIGTERM`, and the lock frees anyway
  the moment the Postgres connection drops — `SIGKILL` included. The standby
  wins the lock on its next poll either way.
- **What it does buy is drain headroom.** On `SIGTERM` the server gives
  in-flight HTTP requests up to 10 s to finish, then drains the dispatch pool so
  dispatches already in progress settle instead of leaving task instances stuck
  `queued`. Reapers are gated off during the step-down so nothing destructive
  fires from a dying leader.
- **With tasks running, the stop is bounded and completes within the grace.**
  Open agent log streams are closed and flushed the moment `SIGTERM` arrives
  (the agent keeps running its task; log shipping is best-effort), and the gRPC
  graceful stop that follows is bounded at 5 s before falling back to a forced
  stop — which still lets the remaining handlers finish their deferred flushes.
  HTTP (≤10 s) + dispatch drain (≤15 s) + gRPC stop (≤5 s) fits the default
  30 s, so the pod exits on its own; a `SIGKILL` (`exitCode: 137` on the
  terminated container) now means something else exceeded its bound and is
  worth a look. Nothing in the leadership handoff needs any of this to finish.

The HA profile sets `60` for comfortable HTTP + dispatch drain headroom under
load. The chart deliberately ships **no default**: a default would add up to
30 s of downtime to every single-replica `Recreate` upgrade, for nothing.

## Involuntary disruptions: why HA is the posture that matters

No PDB, annotation or grace period prevents:

- **node failure** — hardware, kernel panic, kubelet or container-runtime crash;
- **spot / preemptible reclamation** — a two-minute notice at best, no eviction
  API involved;
- **zone or network partition** that isolates the node;
- **platform-side forced maintenance** once a bounded PDB window expires.

Each of those is the 48–80 second restart window with no warning. The only
thing that shrinks it is a replica that is already running somewhere else — and
the scheduler's own resilience — the post-leadership grace on the agent-lost and
pod-lost reapers, the destructive gate that holds every reaper during a step-down,
the reconciler that recovers a pod's durable outcome, and the boot-time check of
the timing ladder they depend on, described in
[scheduler resilience](/operate/scheduler-resilience/) — is what keeps the tasks
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
