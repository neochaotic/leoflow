# ADR 0042: dbt support via native-Go manifest rendering

**Status:** Accepted — shipped in v0.1.1 (dbt-native manifest rendering); extended by ADR 0043 (TaskGroup split/fused execution) and ADR 0044 (multi-project by domain)
**Date:** 2026-06-24
**Companions:** ADR 0003 (DAG as immutable artifact), ADR 0024 (parser shim), ADR 0038 (connector dependency ergonomics), ADR 0040 (operator support), ADR 0041 (`leoflow deploy`), the editions split (Lite/Pro)

## Context

dbt + an orchestrator is one of the most-requested data-engineering combinations,
and the most common reason teams reach for Airflow at all. Supporting dbt well is
high-leverage for adoption.

The de-facto way to run **dbt Core** under Airflow is **Astronomer Cosmos**, which
reads a dbt project and generates Airflow tasks (a `DbtTaskGroup`/`DbtDag`, one
task per model/seed/snapshot/test). Cosmos does **not** drop into Leoflow:

1. **It is a third-party DAG-authoring library imported at parse time.** Our parser
   executes the DAG against a dependency-free structural shim (ADR 0024); `import
   cosmos` is a `ModuleNotFoundError` there.
2. **It builds a `TaskGroup` and, in several render modes, uses dynamic task
   mapping.** Our compiler emits flat tasks and rejects both (`compiler.py`,
   `sdk/__init__.py`).
3. **Its operators are `cosmos.operators.*`, not `airflow.providers.*`,** so the
   generic operator capture path (ADR 0040) does not claim them.
4. **It re-parses `manifest.json` on every `DbtDag` init** — a documented, "massive"
   penalty on the KubernetesExecutor, which is exactly Leoflow's pod-per-task model
   (astronomer/astronomer-cosmos#2294).

The key realization: **the dbt DAG is already a JSON artifact that dbt itself
produces** — `target/manifest.json`, with every node's `resource_type`, `name`, and
`depends_on.nodes`. Cosmos's core job is to read that file. Leoflow can read the
same file **in Go, at compile time**, and emit flat `dag.json` tasks — no Cosmos in
the hot path, no Airflow in the parser, no TaskGroup/dynamic-mapping gap to fill.

## Decision

### 1. Render dbt to `dag.json` natively in Go — Cosmos is a reference, not a dependency

`leoflow compile` reads `manifest.json`, walks `nodes`, and emits one Leoflow task
per executable dbt node (`seed`, `model`, `snapshot`, `test`), wiring `depends_on`
from each node's `depends_on.nodes` (filtered to nodes that became tasks). Each task
is a native Leoflow `bash` task running the node's scoped dbt command:

| dbt resource | task command |
|---|---|
| `seed` | `dbt seed --select <name>` |
| `model` | `dbt run --select <name>` |
| `snapshot` | `dbt snapshot --select <name>` |
| `test` | `dbt test --select <name>` |

This aligns with **python-minimal-go-max** and **simple-reliable-then-grow**: the
graph logic is Go reading well-documented JSON; no provider runtime is loaded to
build the DAG.

### 2. The warehouse is the shared state between tasks

Pod-per-node is correct because tasks do **not** share a filesystem but **do** share
the external warehouse (Snowflake/BigQuery/Postgres/Redshift). Running each node as
its own scoped dbt invocation, in topological order, against the shared warehouse,
produces the same result as a monolithic `dbt build` (spike-verified, below).

### 3. Where dbt-core lives

dbt-core + the adapter (`dbt-snowflake`, `dbt-bigquery`, …) is a **Python runtime
dependency**, declared like any connector (ADR 0038). It lands wherever tasks run:

- **Lite:** the Lite-managed venv.
- **Pro:** the DAG image (`FROM leoflow-runtime` + `pip install dbt-core dbt-<adapter>`),
  baked alongside the project files.

At **compile time**, the `manifest.json` is produced by `dbt parse` — which is
**offline** (it resolves refs/sources and compiles Jinja without a warehouse
connection; spike-confirmed). The exception is a project that introspects the
warehouse at parse time (`run_query`/`adapter.*` macros, introspected sources);
those builds need credentials. This is surfaced, not silently worked around.

### 4. Manifest generation differs by edition — but is the same logical step

The pipeline is always `dbt parse → manifest.json → dag.json`. *When* and *where*
it runs is the edition difference:

| | **Lite** | **Pro** |
|---|---|---|
| dbt project | workspace `~/leoflow/<dag>/` | baked into the DAG image |
| manifest generated | `leoflow compile` / hot-reload runs `dbt parse` **locally, JIT** on each change | **once, at image-build time in CI** (part of `leoflow deploy`) |
| manifest lives | `target/` of the workspace | **baked into the image** (+ `partial_parse.msgpack`), immutable with the artifact |
| `dag.json` source | the JIT manifest | the **baked** manifest |

In **Pro**, the manifest becomes **part of the immutable artifact** (ADR 0003: the
`dag.json` + image are versioned together, pinned by digest, never mutated). A single
`dbt parse` at build time feeds **both** the `dag.json` and the image, so the two
cannot drift. Runtime pods never re-parse — they find the manifest already in the
image.

**Bring-your-own-manifest:** teams that already emit `manifest.json` in their own dbt
CI may supply it directly (analogous to Cosmos `LoadMode.DBT_MANIFEST`); Leoflow then
skips `dbt parse`.

### 5. Granularity is configurable — fine in Lite, grouped in Pro

The spike measured the per-invocation overhead of `dbt <verb> --select` at **~1.5s**,
**roughly constant** with project size — it is dominated by **dbt/Python process
startup + adapter import**, not project parse. Baking a warm `partial_parse` saves
only ~0.3s, so the lever is **not** "ship the manifest" — it is **how many dbt
processes we spawn**. One pod per node multiplies 1.5s (plus, in Pro, pod scheduling
+ image pull + agent gRPC handshake) by N.

Therefore granularity is a knob (`dbt.granularity` in `leoflow.yaml`), not a fixed
"1 model = 1 task". The spike (50 models) measured per-layer grouping at **2 pods /
5.4s** vs per-node **50 pods / 77s** vs monolithic **1 pod / 3.2s** — a 14× cut over
per-node for 1.7× the monolithic floor.

#### The grouping rule

A grouping is a **partition** of the executable nodes (seed/model/snapshot/test) into
groups; each group is contracted into one Leoflow task (a **quotient graph**):

- **task_id** = the group name.
- **command** = `dbt build --select <group expression>`. We use `build`, not `run`:
  it runs the group's seeds/models/snapshots/tests in dbt's own internal topological
  order in a single invocation, so a group may mix resource types.
- **depends_on** = quotient edges: `Gi → Gj` iff some node in `Gi` has a child in
  `Gj` (i ≠ j); intra-group edges are dropped (dbt orders them internally).

Partition strategies:

| `granularity` | partition | validity |
|---|---|---|
| `node` | each node its own group (identity) | always valid — **Lite default** |
| `level` | topological depth: `level(n) = 1 + max(level(parents))` | **acyclic by construction** — **Pro default** |
| `resource` | all seeds / all models / all tests | acyclic by construction |
| `folder` | by model directory (`fqn[1]`) | **semantic → must be validated** |
| `tag` | by dbt `tag:` | semantic → validated (+ overlap rule) |
| `selector` | named groups in `leoflow.yaml` | validated + coverage |

**The correctness invariant: the quotient graph must be acyclic.** This is the
non-obvious part — *a grouping can introduce a cycle between groups even when the node
graph is a clean DAG*. Example: a clean chain `raw → stg_a → mart_a → stg_b` is a valid
node-DAG, but grouping by folder puts `stg_a, stg_b` in `staging` and `mart_a` in
`marts`, yielding `staging → marts` (via `stg_a → mart_a`) **and** `marts → staging`
(via `mart_a → stg_b`) — a 2-cycle.

So the renderer always builds the quotient and:
- For **construction-safe** strategies (`node`, `level`, `resource`) — acyclic by
  construction, no check needed.
- For **semantic** strategies (`folder`, `tag`, `selector`) — detect a quotient cycle
  and **reject loudly** (simple-reliable: *loud reject > silent wrong*), naming the
  cycle and the offending groups, suggesting `granularity: node` or a re-org.

Two further invariants on the partition:
- **Coverage** — every executable node lands in exactly one group (no orphan).
- **No overlap** — a model with two grouping tags would belong to two groups, breaking
  the partition. Rule: reject, or require a single dedicated `group:<name>` tag per node.

Config:

```yaml
dbt:
  granularity: folder        # node | level | resource | folder | tag | selector
  groups:                    # only for granularity: selector
    - { name: staging, select: "path:models/staging" }
    - { name: core,    select: "tag:core" }
```

Defaults: **Lite → `node`** (cheap subprocess, warm cache, per-model observability in
the inner loop); **Pro → `level`** (construction-safe), with `folder`/`tag` available
when a team wants semantic groups and the cycle check protecting them.

#### Failure isolation — the failure domain follows the grouping

At run time a broken model must not take down unrelated ones. The guarantee is
granularity-dependent, and the spike measured both cases:

- **`node` granularity — full isolation, already provided.** Each model is its own
  Leoflow task/pod. A failing model fails only its task; the scheduler + trigger rules
  block only its downstream subtree, and independent branches run unaffected. This is
  existing Leoflow task behavior (pod-per-task + trigger rules), not new work.
- **Grouped granularity — coarsened failure domain.** A group runs as one
  `dbt build --select <group>` invocation. dbt (default, no `--fail-fast`) still builds
  the group's independent good models and writes them to the warehouse, skipping only
  its own dbt-downstream, and `run_results.json` carries per-node status. **But the
  group task exits non-zero**, so Leoflow marks the whole group failed and blocks
  everything downstream of the group — including parts that individually succeeded.
  Spike: a `marts` group with `mart_ok` + a broken `mart_bad` → `mart_ok` materialized
  (`success`), `mart_bad` errored, dbt exit 1, group task = failed.

This is the inherent cost of grouping (Cosmos shares it): fewer pods, larger blast
radius. The defaults reflect it — Lite uses `node` (full isolation in the dev loop);
Pro uses `level` for cost, but a team needing strict per-model isolation chooses `node`
and accepts the pod count.

**Build-time breakage is fine and loud.** A compile-broken model (bad `ref`, Jinja
error, missing dependency) fails `dbt parse` at CI build time → no manifest → no
artifact → deploy blocked. That is the desired hard stop. Boundary: parse catches
*compile* breakage; a SQL error that only surfaces at execution (e.g. a missing column)
passes parse and is a run-time failure, handled per the granularity rules above.

### 6. Node identity

Tasks key off the dbt `unique_id` (`model.<pkg>.<name>`) to avoid collisions across
resource types and packages; the human `task_id` is derived from the name with the
resource type as needed for uniqueness.

## Evidence (spike — `scratchpad/dbt-poc`, throwaway)

A real dbt-duckdb project (seeds → staging → marts with fan-in + tests) plus a
~90-line Go mapper and a runner proved:

- **Graph extraction:** `manifest.json` → flat `dag.json` with correct edges,
  including fan-in (`customers` ← `orders` + `stg_customers`) and tests depending on
  their model.
- **Execution correctness:** running the 8 nodes as **separate** `dbt --select`
  invocations in topological order yields a warehouse **identical** to a monolithic
  `dbt build` (`PASS — pod-per-node == monolithic build`).
- **`--select` overhead:** ~1.5s/invocation, ~constant (1.48s @ N=10, 1.53s @ N=50);
  startup-bound, not parse-bound. Monolithic vs per-node: 8.6× (N=10), 23.9× (N=50).
  Extrapolated pure-startup penalty: ~2.5 min @ 100 models, ~12.7 min @ 500.
- **Grouping payoff:** per-layer cuts 50 pods → 2 and 77s → 5.4s.
- **Grouping validator:** a Go quotient-graph builder was run on a trap project
  (clean chain `raw → stg_a → mart_a → stg_b`). Grouping by `folder` is **rejected**
  with the cross-group cycle named (`marts → staging → marts`); `level` and `node`
  are **accepted**; and `folder` on a clean staging→marts project is **accepted**
  (3 tasks). The one subtle correctness risk of grouping is covered.
- **Failure isolation:** in a grouped task (`dbt build --select marts`) with an
  independent good + broken model, the good model still materialized (`success`) while
  the broken one errored — but dbt exited 1, so the group task fails as a unit, and
  per-node status is in `run_results.json`. Confirms node granularity isolates fully and
  grouping coarsens the failure domain (not loses the good writes).
- **Offline parse:** `dbt parse` with the warehouse path pointed at a non-existent
  directory returned rc=0 and still produced `manifest.json`.

## Open questions — what still warrants a PoC before/during implementation

The core hypotheses are proven; these are **not** yet and are load-bearing:

1. **Real Leoflow wiring (integration spike).** The spike used a standalone runner.
   Validate the full path: `leoflow compile` (reading a baked manifest) → `push` →
   a real pod from an image with dbt-core + the project baked, running
   `dbt run --select X` against a warehouse and reaching `success` via the agent.
   This is the ADR-0040-style spike that turns "works in isolation" into "works in
   Leoflow." **Highest priority.**
2. **Incremental models + snapshots across separate pod invocations.** `is_incremental()`
   and snapshot merge logic read the node's existing table (warehouse state). Should
   work under a shared warehouse, but `--full-refresh` semantics and first-vs-subsequent
   runs across independent invocations need a PoC — real projects lean on incrementals.
3. **Failure-domain & test semantics in Leoflow.** Confirm end-to-end in a real run
   that `node` granularity isolates fully (a failed model fails only its task; siblings
   succeed; only its subtree is blocked), and that a failing `dbt test` maps to the
   right task failure + downstream trigger-rule behavior. Separately, decide how much of
   a grouped task's `run_results.json` to surface in the UI (per-node status, partial
   warehouse progress) so a coarse group failure is still observable per model.
4. **Selector fidelity for grouping.** Grouping by `tag:`, `path:`, and graph operators
   (`model+`, `+model`) must map faithfully from the manifest to the group commands.

## Deferred (explicitly out of scope for v1)

- **Compiled-SQL-direct execution** — run `target/compiled/*.sql` with a thin warehouse
  client, eliminating the ~1.5s dbt boot per node entirely. Tempting for very large
  projects, but it sheds dbt semantics (run-results, hooks, tests, incremental logic).
  Revisit as a separate ADR only if grouping proves insufficient; PoC then.
- **dbt Cloud** — orchestrating dbt Cloud jobs is already reachable via the generic
  operator path (ADR 0040, `apache-airflow-providers-dbt-cloud`). This ADR is dbt Core.
- **Exposures / metrics / semantic models** as tasks.

## Documentation deliverable (ship-blocking, per the connector definition-of-done)

dbt support ships with a GitHub Pages cookbook page: a working dbt DAG + `leoflow.yaml`
(connector declaration for `dbt-<adapter>`, `dbt.granularity`), the Lite vs Pro flow,
the manifest/CI story, and troubleshooting (parse-time warehouse credentials, granularity
tuning, bring-your-own-manifest). The page must run end-to-end, not be a snippet.

## Consequences

- A dbt project becomes a first-class Leoflow DAG with **no Cosmos, no Airflow in the
  parser, and no new dag.json constructs** (no TaskGroup/dynamic-mapping required).
- The pod-per-task overhead that hurts Cosmos is **acknowledged and managed** via a
  granularity knob, with measured numbers behind the defaults.
- The manifest fits the immutable-artifact model cleanly: built once at deploy time,
  baked, digest-pinned, never re-parsed at runtime.
- New surface to own: a manifest→dag.json renderer in Go, a `dbt.granularity` config,
  and dbt-core/adapter as declarable connectors.
