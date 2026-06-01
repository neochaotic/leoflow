# Map-reduce in Leoflow

> One pattern, every parallel workflow: a fan-out of independent tasks that
> a single downstream task **reduces** into a result. Hyperparameter search,
> k-fold cross-validation, batch inference, ETL shard aggregation —
> Leoflow expresses them all in two lines of Python.

## The pattern

```python
from airflow.sdk import DAG, task

@task
def trial(lr: float) -> dict:
    return train_one(lr)          # map: pure per-shard compute

@task
def select_best(trials: list[dict]) -> dict:
    return max(trials, key=lambda r: r["score"])   # reduce: combine results

with DAG("hparam_search", schedule=None):
    select_best([trial(lr) for lr in [0.001, 0.01, 0.05, 0.1, 0.5]])
```

That `[trial(lr) for lr in …]` is the whole map. The argument `trials: list[dict]`
is the whole reduce. **No XCom plumbing, no broker setup, no shared filesystem,
no special operator.** Just a list comprehension and a function that takes a list.

At runtime:

1. Leoflow fans out — every `trial(lr=…)` is a separate task instance running
   in parallel (its own pod on Pro; its own subprocess on Lite).
2. Each map task returns its result; Leoflow stores it as XCom keyed by
   `(dag_id, run_id, task_id, return_value)`.
3. When all upstreams finish, Leoflow dispatches `select_best`. The agent
   fetches every upstream's XCom, assembles them into a JSON array (in
   declaration order), and stamps it as `LEOFLOW_XCOM_TRIALS`.
4. The runtime delivers the list directly to your function.

## Why this matters for ML

Most ML workloads are map-reduce: independent work per shard, then one
aggregator. Leoflow's contract makes this cheap and durable:

| ML pattern | Map | Reduce |
|---|---|---|
| Hyperparameter search | one task per `(lr, batch, seed)` triple | pick the best metric |
| K-fold cross-validation | one task per fold | average the metrics |
| Ensemble training | one task per base model | combine predictions / stack |
| Sharded preprocessing | one task per data shard | concatenate / merge index |
| Batch inference | one task per partition | collect predictions to a sink |
| Monte-Carlo simulation | one task per worker | average / sum results |

The Leoflow runtime is **container-native** (pod-per-task on Pro, subprocess-per-task
on Lite), so every map task gets a clean process with its own memory, deps, and
GPU slice when you ask for one. No shared interpreter, no GIL contention, no
"why did training 7 leak memory into training 9."

## Reliability — what fan-in buys you

- **Per-trial isolation.** A crash in trial 3 doesn't take down trials 0–2 or
  4–9. Their XComs are already pushed; the reducer sees `null` in slot 3 and
  decides what that means.
- **Per-trial retry.** Trial 7 fails on attempt 1, retries on attempt 2,
  succeeds. The reducer only runs once everyone's terminal — and only sees
  successful results unless you opt into `trigger_rule="all_done"`.
- **Resume after a restart.** XComs are durable; if the reducer crashes or
  the control plane restarts, the next run can clear-and-rerun just the
  reducer without re-doing the expensive map step.
- **Deterministic ordering.** The reducer receives upstreams in the
  declaration order of the list comprehension, not the order they finished.

## What the parser captures (under the hood)

For

```python
select_best([trial(lr) for lr in [0.001, 0.01, 0.05, 0.1, 0.5]])
```

the parser writes this into `dag.json`:

```json
{
  "tasks": [
    {
      "task_id": "select_best",
      "depends_on": ["trial", "trial__1", "trial__2", "trial__3", "trial__4"],
      "xcom_input": {
        "trials": ["trial", "trial__1", "trial__2", "trial__3", "trial__4"]
      }
    },
    {"task_id": "trial",    "call_args": {"lr": 0.001}},
    {"task_id": "trial__1", "call_args": {"lr": 0.01}},
    {"task_id": "trial__2", "call_args": {"lr": 0.05}},
    {"task_id": "trial__3", "call_args": {"lr": 0.1}},
    {"task_id": "trial__4", "call_args": {"lr": 0.5}}
  ]
}
```

- `depends_on` is the dependency edge — what scheduler waits on.
- `xcom_input.trials` is the chain-of-custody for the reducer's `trials`
  parameter — the agent fetches each task's return value and assembles them
  in this exact order.
- `call_args.lr` is the literal each trial gets — captured at compile time
  so the worker doesn't need the DAG-build context at runtime.

## End-to-end example

`examples/ml_hparam_search/` ships a runnable version of the snippet at the
top. It uses a toy quadratic instead of a real model so it runs in
milliseconds:

```bash
leoflow lite                              # boot Lite
# UI → DAGs → ml_hparam_search → Play
```

In the run page you will see five `trial` tasks fan out in parallel, then
`select_best` fires once all five succeed. Open `select_best`'s logs to see
the chosen `lr` and its score; open the XCom tab on any task to see its
return value.

## Beyond the basics

- **`max_active_tasks`** caps how many map tasks run in parallel (cluster
  pressure, GPU slots). Set it on the DAG.
- **`retries`** + **`retry_delay_seconds`** make each map task survive
  transient infrastructure flakes.
- **Per-task `resources`** in `leoflow.yaml` give each trial its own CPU /
  memory / GPU budget.
- **`trigger_rule="all_done"`** runs the reducer even when some trials fail,
  letting you decide what `null` in `trials` means.

## Roadmap

- Dynamic mapping (the parser auto-detects `[f(x) for x in <runtime-list>]`)
  and lifts the fan-in degree from "compile-time literal" to "discovered at
  run-time." Coming after the alpha cut.

## Reference DAGs

| Example | Map | Reduce |
|---|---|---|
| [`examples/ml_hparam_search/`](https://github.com/neochaotic/leoflow/tree/main/examples/ml_hparam_search) | toy training × 5 LRs | best score |
| [`examples/fan_out_aggregate/`](https://github.com/neochaotic/leoflow/tree/main/examples/fan_out_aggregate) | sum a slice of integers × 4 shards | total |
| [`examples/montecarlo_pi/`](https://github.com/neochaotic/leoflow/tree/main/examples/montecarlo_pi) | sample inside the unit circle × 4 workers | π estimate |
