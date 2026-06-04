# ADR 0040: Airflow operator + sensor execution — native fast path + generic executor

**Status:** Accepted (design; implementation post-RC, phased)
**Date:** 2026-06-04
**Companions:** ADR 0038 (`connectors:` sugar), ADR 0039 (generated connector catalog), ADR 0024 (parser structural shim), ADR 0036 (runtime compat shim — deferred), ADR 0022/0023 (task config)

## Context

Today Leoflow accepts a **closed set of three task types** — `python` (`@task`/
`PythonOperator`), `bash` (`BashOperator`), `http_api` (`HttpOperator`, a
lightweight inline subset). Everything else is a **loud compile error**. The
parser uses a structural shim (ADR 0024) that mocks only those operators; the
runtime re-implements each natively.

The connectors work (ADR 0038/0039) made every Airflow **hook** usable inside a
`@task`. But provider **operators and sensors** — the task types themselves
(`SnowflakeOperator`, `DataformCreateRepositoryOperator`, `S3KeySensor`, …) — are
still rejected. The goal of this ADR is **full integration**: run any Airflow
operator/sensor, not a hand-picked subset.

Operators ≠ connectors *conceptually*, but at the **pip level they are the same
unit** — the provider package is monolithic. `apache-airflow-providers-google`
(v22) ships **325 submodules**: all 563 GCP operators **and** the connection
types and hooks. So `connectors: [google_cloud_platform]` already installs every
GCP operator/sensor; they simply cannot be used as task types yet.

### The dimension (why native-only does not scale)

Across the 42 (of ~90) providers we generate the catalog from: **~760 operators,
82 transfers, 148 sensors**. `google` ≈ 563, `amazon` ≈ 200 (~75% of the mass).
Full ecosystem ≈ **1,500+ operators**. Re-implementing them natively is
impossible — three today, never fifteen hundred.

### Spikes (2026-06-04) — against real `apache-airflow==3.2.1` + task-sdk

**Operators:**
- `BashOperator.execute(context={})` runs **standalone** (no scheduler/DagRun).
- `SQLExecuteQueryOperator` runs standalone against a real Postgres, resolving
  its connection from `AIRFLOW_CONN_*` (**the env-secrets mechanism the connectors
  work already delivers**) → `[(42,)]`.
- `{{ ds }}` passes through literally without `render_template_fields(context)` —
  the one known gap (Jinja).

**Sensors:**
- `mode='poke'`, immediate success → `execute(context={})` runs the poke loop
  standalone and returns. **Same generic path as operators.**
- `mode='poke'`, timeout → raises `AirflowSensorTimeout` → a normal task failure.
- `mode='reschedule'` → raises `AirflowRescheduleException` and reaches for the
  TaskInstance's reschedule history — i.e. it needs **control-plane support**
  (persist next-poke time, free the pod, re-dispatch).

Verdict: generic execution is **feasible and cheap** for synchronous operators
**and poke-mode sensors** (`import_string(class)(**kwargs) → render_template_fields
→ execute`). Reschedule sensors need scheduler work. Deferrable operators (the
async triggerer) are not yet spiked.

## Decision

**A hybrid dispatcher keyed on the dag.json `type`, with the provider — not the
operator — as the install unit, and a phased path to full integration.**

1. **Native fast path.** `bash`/`http`/`python` today, run by our own Go/runtime
   code (fast, no Python in the hot path). A deliberate, **growing whitelist**:
   over time we migrate the *most-used* operators into native implementations as
   usage justifies the perf / zero-Python win. **Sensors are the prime target
   here** (see *Native sensors via goroutines* below): "wait until a condition"
   is exactly what a goroutine does cheaply, so the long-term native sensor path
   beats both of Airflow's modes.

2. **Generic executor (breadth).** Any operator/sensor not on the native
   whitelist is run by **one** generic executor in the task pod:
   `import_string(class)(**kwargs)` → `render_template_fields(context)` →
   `execute(context)` → push the return to Leoflow XCom. **One executor covers all
   ~1,500 operators and all poke-mode sensors.** Native-first: a `type` with a
   native impl uses it; otherwise it falls through to generic.

3. **Provider is the unit, auto-derived from the import.** The operator import
   path names its provider (`airflow.providers.google.cloud.operators.dataform` →
   `apache-airflow-providers-google`), resolved with the **same curated boundary
   the catalog already encodes** (`amazon.aws` → `amazon`; `microsoft.mssql` →
   `microsoft-mssql`). **No per-operator table** — mapping 1,500 by hand is absurd
   and drifts every release. Declaration reuses `connectors:`/`dependencies:`.

### Phased roadmap to "integrate everything"

| Phase | Covers | Mechanism | Cost |
|---|---|---|---|
| **A** | sync operators (~760) **+ poke-mode sensors** | generic executor in the pod (spike-proven) | ~2–3 weeks |
| **B** | **reschedule-mode sensors** | Go scheduler catches `AirflowRescheduleException`, persists next-poke (a `task_reschedule` equivalent), frees the pod, re-dispatches at the due time | medium, bounded |
| **C** | **deferrable operators** (`execute` → `defer` → trigger) | a **triggerer** execution model (asyncio event loop running `Trigger.run()`); **needs its own spike** before sizing | heavy |
| **D** | **dynamic task mapping / task groups** | parser+scheduler **expansion** (N mapped TIs), not the executor — a separate track | medium–heavy |

Until a phase lands, its constructs stay a **loud compile reject** — never a
silent mistranslation (the standing principle). The phases are the plan to retire
each reject, not a permanent exclusion.

### Native sensors via goroutines (future direction)

The Phases A/B above run sensors *the Airflow way* (a Python `poke` loop in a
pod, or reschedule churn). The **long-term native answer is better and Go-shaped**:
a sensor is "block until a condition is true", which is exactly a cheap
**goroutine** in the control plane — one goroutine per waiting sensor, polling
the condition on its interval, **holding no pod and needing no `task_reschedule`
table**. Thousands wait concurrently for the cost of a goroutine each. This is
where the most-used sensors migrate as part of the native whitelist, superseding
both Airflow modes for them. The generic Python path (Phase A) remains the
fallback for the long tail of sensors we have not promoted. *Present state: the
closed native set; the goroutine sensor engine is future work.*

## Design (pieces, sized against the spikes)

1. **Parser** — a generic catch-all for `airflow.providers.*.operators.*`,
   `.sensors.*`, `.transfers.*`: capture class path, constructor kwargs, the
   `template_fields`, and (for sensors) `mode`. Emit `type: "airflow_operator"`
   (or `"airflow_sensor"`). Provider derived from the dotted import. **M.**
2. **dag.json** — `{operator_class, args, templated_fields, mode?}`. **S.**
3. **Runtime generic executor** — `import_string` + a minimal context (`run_id`,
   `ds`/`ts` macros, `params`, a `ti` shim for xcom) + `render_template_fields` +
   `execute` + XCom push; map `AirflowSensorTimeout` → failure. **S–M.**
4. **Scheduler (Phase B)** — reschedule state + re-dispatch on the
   `AirflowRescheduleException` next-poke date. **M.**
5. **Triggerer (Phase C)** — separate, post-spike.
6. **Compile validation** — detect `from airflow.providers.X…` operators/sensors
   → require `connectors:`/`dependencies:` (the ADR 0038 #2 scan, now higher-value
   because operators/sensors are declared top-level). **S.**
7. **Native promotion ramp** — operators graduate generic → native when usage
   warrants it. Three today; grow deliberately.

### UX (integration with `connectors:`)

- **One declaration installs the provider → hooks, operators AND sensors.**
  `connectors: [snowflake]` (or `dependencies:`) unlocks `SnowflakeOperator`
  (task), `SnowflakeHook` (inside `@task`), and any snowflake sensor.
- **Operators/sensors are top-level** (they *are* the task), unlike hooks
  (imported inside `@task`). The parser captures them in place.
- **Optional, later:** a `providers: [google]` sibling (more honest than
  `connectors:` when the intent is an operator, not a connection); and an opt-in
  "batteries-included" fat base image for convenience over size — neither the
  default (the default stays the lean per-DAG image).

## Consequences

- **Breadth in one move:** ~3 → ~1,500 operators + poke sensors via a single
  generic executor (~2–3 weeks, de-risked by the spikes), **faithful to the
  thesis** — the task runs in the pod on the real task-sdk; the Go control plane
  is untouched and we do **not** re-adopt Airflow's serialization layer.
- **Migration compatibility:** existing Airflow DAGs using provider operators run
  mostly as-is once their provider is declared.
- **Fidelity caveats (named):** the runtime must build the templating context
  faithfully (`ds`/`ts`/`params`/`conn`); some operators read more context keys
  (`ti`, `dag_run`, `macros`); `do_xcom_push` semantics must match. Bounded,
  surfaced per-operator as coverage widens.
- **Poke sensors hold a pod** while blocking (acceptable per-task; reschedule mode
  in Phase B frees the slot for long waits).
- **Maintenance:** the generic path tracks a narrow, stable Airflow surface
  (`execute`/`render_template_fields`/`poke`), far cheaper than per-operator native
  code. The native whitelist is the only hand-maintained part.

## Status of work

Operator and sensor (poke) spikes are done (recorded here). Implementation is
**deferred to post-RC**: the closed native set is unchanged for the first
release. Phase C (deferrable/triggerer) needs its own spike before it is sized.
This ADR is the executable plan for picking up full operator/sensor integration.
