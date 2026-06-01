# Architecture

```mermaid
flowchart LR
  subgraph Dev["Dev / CI"]
    A[leoflow.yaml + dag.py] -->|leoflow compile| B[dag.json + image]
  end
  B -->|leoflow push| API
  subgraph CP["Control plane (Go)"]
    API[HTTP API /api/v2, /ui] --- SCH[Scheduler<br/>state machine]
    SCH --- EXR[Executor router]
  end
  EXR -->|client-go| K8S[(Kubernetes<br/>pod-per-task)]
  EXR -->|dev| SUB[Subprocess]
  K8S --> POD[Worker pod<br/>agent ⇄ gRPC ⇄ user code]
  CP --- PG[(Postgres<br/>metadata + Lite XCom/locks)]
  CP -. Production only .- RD[(Redis<br/>XCom + locks)]
```

**Control plane (Go).** Gin HTTP serving the Airflow-compatible `/api/v2/*` and
`/ui/*`; a goroutine-based scheduler (state machine, leader-elected via Postgres
advisory locks — [ADR 0009](adr/0009-leader-election.md)); an
executor router (Kubernetes via client-go, subprocess for dev, inline for
http_api — [ADR 0002](adr/0002-pod-per-task.md)).

**Worker pod.** Each task runs in its own pod from the DAG's image. The
**agent** (Go, PID 1) talks gRPC to the control plane: fetches the task spec,
runs the user code, streams logs, pushes XCom, reports state.

**State.** Postgres holds metadata for every edition; on **Lite** it also holds
XCom and the scheduler's advisory locks (no Redis required — [ADR 0026](adr/0026-lite-datastore-no-redis.md)).
On **Production**, Redis stores XCom (≤256 KB) and the multi-node locks.

**Stack:** Go 1.26 · Gin · sqlc/pgx · golang-migrate · client-go · gRPC ·
log/slog · Prometheus · OpenTelemetry · Cobra · Viper. Python only in the DAG
parser sidecar and inside user task containers.

## Map-reduce (fan-in) data flow

Leoflow treats *N independent tasks → 1 aggregator* — the map-reduce
topology that dominates ML and batch pipelines — as a first-class shape
in the DAG. The activation criterion is purely syntactic: the parser
captures fan-in when **a parameter is bound to a list (or tuple) where
every element is a task call**.

```python
# Activates fan-in (3 equivalent forms):
select_best([trial(lr) for lr in LRs])      # list comprehension
combine([estimate(0), estimate(1), ...])    # explicit list
report((extract_a(), extract_b()))          # tuple

# Does NOT activate fan-in:
transform(extract())            # single upstream — normal TaskFlow path
shard(n=0)                      # literal kwarg → captured as call_args
start >> [a, b, c]              # dependency edge only, no arg binding
f(items=[1, 2, 3])              # list of literals → call_args JSON
```

The pipeline:

1. **Parser** (`_bind_call_arguments`) inspects the bound arguments at the
   call site. If every element of a `list`/`tuple` argument is a
   `XComArg`, it records `xcom_input[param] = [upstream_task_id_1, …,
   upstream_task_id_N]` in `dag.json`. Single-upstream is also a list
   (1-element) so the schema is uniform.

2. **Scheduler** sees the dependency edges in `depends_on` and dispatches
   the N map tasks in parallel. When all are terminal under the
   reducer's trigger rule (default `all_success`), the reducer enters
   `queued`.

3. **Agent** (in the reducer's pod / subprocess), upon `GetTaskSpec`,
   receives `xcom_input_mapping: {param: XComUpstreams{task_ids: [...]}}`.
   For each parameter it fetches every upstream's `return_value` via the
   existing `FetchXCom` gRPC (N round-trips), assembles the values into
   a JSON array in **declaration order**, and stamps
   `LEOFLOW_XCOM_<PARAM>` with the array. A missing upstream contributes
   `null` so the reducer always receives `len(upstreams)` elements.

4. **Runtime** (`_resolve_kwargs`) JSON-decodes the env var; the reducer
   function receives the list directly as its parameter — no XCom API
   call inside the user code.

The wire format on the agent contract is
`map<string, XComUpstreams>` (a proto wrapper), not a delimited string or
a polymorphic value — see [ADR 0034](adr/0034-fan-in-map-reduce-binding.md)
for the full design + non-options considered. The cookbook page at
[map-reduce.md](cookbook/map-reduce.md) covers user-facing guarantees
(scope, order, null semantics, the 256 KB ceiling, the dynamic-mapping
roadmap gap).

See the [Architecture Decision Records](adr/0001-why-leoflow.md) for the *why*.
