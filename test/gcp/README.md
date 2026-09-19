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
| `pod-per-task` | 10 x `e2-standard-4` | 6h | ~USD 8.60 |
| `warm-pool-ab` | 3 x `e2-standard-4` | 6h | ~USD 3.00 |
| `netpol` | 3 x `e2-standard-4` | 2h | ~USD 1.00 |
| **all three, once** | | | **~USD 13** |

The authorized ceiling is USD 50, which buys roughly three full passes. **The
extra headroom is not a reason to spend it.** More nodes do not make these
answers truer: the saturation experiment is the only one whose result depends on
node count, and the other two would just idle hardware. What the headroom really
buys is **re-runs**, and re-runs are worth paying for, because one run is one
sample and a single number from a shared cloud is not a measurement.

### The number that actually matters

| | |
|---|---|
| A four-hour window, 3 nodes | ~USD 2 |
| The same cluster left up for a month | ~USD 220 |

Two orders of magnitude. Every guardrail here aims at the second number.

## Guardrails

**`--max-run-duration` on the node pool is the one that counts.** The nodes carry
their own expiry and Google enforces it. If the script dies, if the laptop
sleeps, if the teardown never runs, the expensive part still goes away. Nothing
else in this directory has that property, because everything else depends on
something of ours continuing to run.

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

Nothing about the account is in this repository. Project and zone come from the
environment or `gcloud config`, and every script refuses to guess.

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
```

`teardown.sh --all` removes every cluster this repo labelled, and is the thing
to run when you are not sure.

## What is not here yet

The three runners. `provision.sh` and `teardown.sh` are the safe part and are
done; the experiments themselves need the DAG images built and pushed to a
registry the cluster can pull from, which is the step `warmpool-ab.sh` never
had. Until a runner exists, `provision.sh` gives you a cluster that expires on
its own, which is the right order: the guardrails before the spending.
