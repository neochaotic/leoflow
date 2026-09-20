# Cloud experiments

Three things a local k3d cannot answer, each a bounded window with its own
teardown. The weekend soak stays local and free; this directory exists only for
what genuinely needs a real cluster.

## Why these three, and only these three

| Experiment | Why local cannot answer it |
|---|---|
| `pod-per-task` | The soak runs `leoflow lite --executor subprocess`. The **entire Kubernetes executor path** is therefore unmeasured: pod-per-task, image pulls, the pod informer, the reaper acting on pods. That is the path production runs. |
| `warm-pool-ab` | Warm pools exist only on Kubernetes (`internal/executor`), and Lite's subprocess executor has no pool at all. |
| `netpol` | k3d **accepts NetworkPolicy and ignores it**, so #1089's standing rows pass vacuously. Dataplane V2 enforces, which is the whole point. |

## Budget

Estimated at us-central1 list prices, on-demand, printed by `provision.sh`
before anything is created. Confirm against the pricing calculator; these are
estimates the script uses so an operator sees a figure before approving.

| Experiment | Shape | Window | Estimate |
|---|---|---|---|
| `pod-per-task` | 10 x `e2-standard-4` | 6h | ~USD 8.64 |
| `warm-pool-ab` | 3 x `e2-standard-4` | 6h | ~USD 3.01 |
| `netpol` | 3 x `e2-standard-4` | 2h | ~USD 1.00 |
| **all three, once** | | | **~USD 13** |

Every figure is what `provision.sh --dry-run` prints for that shape, so the table
and the script cannot drift apart. The estimate covers **nodes and the cluster
fee and nothing else**: it excludes boot disks (100 GB of pd-balanced per node by
default, about USD 0.014 an hour each, which is roughly a tenth of the node cost
at ten nodes), egress, load balancers, and anything left behind after the cluster
is deleted. It also ignores any GKE free-tier credit, so it errs high on the
cluster fee and low on everything it omits.

The authorized ceiling is USD 50, which buys roughly three full passes. **The
extra headroom is not a reason to spend it.** More nodes do not make these
answers truer: the saturation experiment is the only one whose result depends on
node count, and the other two would just idle hardware. What the headroom really
buys is **re-runs**, and re-runs are worth paying for, because one run is one
sample and a single number from a shared cloud is not a measurement.

### The number that actually matters

| | |
|---|---|
| A four-hour window, 3 nodes | ~USD 2.01 |
| The same cluster left up for a month | ~USD 361.44 |
| Ten nodes left up for a month | ~USD 1036.80 |

Both of the bottom rows come from `forgotten_usd`, which is `estimate_usd` over
720 hours, so they are the script's own arithmetic rather than a remembered
number. (An earlier draft of this table said USD 220 for the middle row, which
was nothing the script would print.)

Two orders of magnitude. Every guardrail here aims at the second number.

## Guardrails

**`--max-run-duration` on the node pool is the one that counts.** The nodes carry
their own expiry and Google enforces it. If the script dies, if the laptop
sleeps, if the teardown never runs, the expensive part still goes away. Nothing
else in this directory has that property, because everything else depends on
something of ours continuing to run.

Two things about it are worth stating precisely, because the first draft of this
directory got both wrong:

- **It is a NODE-POOL flag.** `gcloud container clusters create` does not accept
  `--max-run-duration`; it exists on `container node-pools create|update`. So
  `provision.sh` creates the cluster, puts the TTL on `default-pool` in a second
  call, **reads it back**, and deletes the cluster immediately if it is not
  there. A cluster whose nodes have no expiry is the one outcome this directory
  exists to prevent, so it is not left standing on a hope.
- **What expiry means has not been verified on a live cluster.** The API says
  `NodeConfig.maxRunDuration` is "the maximum duration for the nodes to exist. If
  unspecified, the nodes can exist indefinitely." Whether GKE then replaces an
  expired node to hold the pool at its target count is not documented anywhere we
  could check, and the first real run is what will answer it. Until then the TTL
  is a backstop, not a substitute for the teardown, and `--list` is still the
  check.

On top of it:

- **A hard refusal above 10 nodes**, because the failure mode of a fat-fingered
  `--nodes` is a bill and not an error.
- **A hard refusal above a 24h TTL.** These are bounded experiments.
- **No autoscaling.** A fixed pool cannot grow into a bill.
- **Zonal, never regional**, because a regional control plane multiplies nodes
  across zones.
- **An unpriced machine type is refused** rather than created with no estimate.
- **The teardown verifies.** It re-describes the cluster after deleting and
  fails loudly if it is still there, because a teardown that trusts its own exit
  code is how a cluster outlives the script that claimed to remove it.
- **The teardown only deletes what this repo labelled**, matching the whole
  `purpose=leoflow-experiment` pair and not a substring. The first version
  matched `purpose=leoflow-experiment-anything` and would have deleted a cluster
  it did not create; its own self-test caught that.

- **A TTL that is not a positive number is refused**, not passed on. Only the
  upper bound used to be checked, so `--ttl -2h` and `--ttl abch` became a
  negative and a zero duration handed to Google.

Nothing about the account is in this repository. Project and zone come from the
environment or `gcloud config`, and every script refuses to guess. The cluster
name `provision.sh` leaves for `teardown.sh` is gitignored: it names a real,
billing cluster and belongs to one run on one machine.

## What a cluster delete does NOT remove

Deleting the cluster is not the end of the bill. Four things live outside it and
survive it:

| Survivor | How it appears | What it costs |
|---|---|---|
| Persistent disks | a PVC that outlived its cluster, which is exactly the shape of leoflow's per-run staging volumes (ADR 0022) | pd-balanced is about USD 0.10 per GB per month, billed whether or not anything is attached |
| Forwarding rules and target pools | a `Service` of type `LoadBalancer` in an experiment | a few USD a month each, plus the address |
| Reserved static addresses | left by the above | billed **while reserved and unused** |
| Artifact Registry repositories | the runners do not exist yet, and the first one will push DAG images somewhere the cluster can pull from | storage per GB per month, indefinitely, and nothing here ever deletes it |

`teardown.sh --leftovers` lists all four. It deletes nothing: none of them
carries the `purpose=leoflow-experiment` label that makes deleting a cluster
safe, so there is no way for a script to tell ours from the project's own, and a
teardown that guesses about that is worse than one that prints a list.

## Spot is deliberately off

Spot nodes are 60% or more cheaper and are the wrong choice here. **A preemption
looks exactly like the infrastructure failure these experiments measure**, so on
a resilience run it is a confound rather than a saving. `--spot` exists for
smoke-testing the scripts themselves and prints a warning saying so.

It also saves less than it looks on a short window: the discount applies to the
nodes, not to the control-plane fee, which dominates a few hours at small node
counts.

## Running one

```bash
export GCP_PROJECT=...        # never committed
export GCP_ZONE=us-central1-a

test/gcp/provision.sh --dry-run --experiment netpol     # plan and cost, creates nothing
test/gcp/provision.sh --experiment netpol --ttl 2h
# ... run the experiment ...
test/gcp/teardown.sh                                    # deletes and verifies
test/gcp/teardown.sh --list                             # what is still billing
test/gcp/teardown.sh --leftovers                        # disks/LBs/registries a delete leaves
```

`teardown.sh --all` removes every cluster this repo labelled, and is the thing
to run when you are not sure. It selects them with a gcloud `--filter`, which is
documented as a word match rather than an exact one for some APIs, so anything
it selects is checked again against the whole `purpose=leoflow-experiment` pair
before it is deleted. A cluster that fails that check is skipped and named, not
deleted and not fatal, so one lookalike cannot abort the sweep.

## What is not here yet

The three runners. `provision.sh` and `teardown.sh` are the safe part and are
done; the experiments themselves need the DAG images built and pushed to a
registry the cluster can pull from, which is the step `warmpool-ab.sh` never
had. Until a runner exists, `provision.sh` gives you a cluster that expires on
its own, which is the right order: the guardrails before the spending.

The registry is the part to design before writing the first runner, not after:
an Artifact Registry repository outlives every cluster that pulled from it, is
not labelled by anything here, and is the one resource in this story that a
teardown will never find on its own. Decide whether the runners create a
repository per run and delete it, or use one long-lived repository that is a
deliberate, known line on the bill.
