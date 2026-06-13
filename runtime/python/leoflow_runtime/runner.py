"""Run a user task callable and capture its return value."""

from __future__ import annotations

import importlib
import inspect
import json
import os
import sys

from leoflow_runtime.xcom import XCOM_ENV_PREFIX

# Lifecycle messages are prefixed so the user can distinguish runtime-emitted
# lines from their own print(). They go to stdout (not stderr) on purpose:
# stderr maps to log level=error in the UI, which would visually scream on
# every successful run.
_LIFECYCLE_PREFIX = "[leoflow]"


def _lifecycle(msg: str) -> None:
    """Emit a one-line lifecycle event the user sees in the UI log panel."""
    print(f"{_LIFECYCLE_PREFIX} {msg}", flush=True)


# NOTE: an earlier iteration wrapped the runtime lifecycle in
# ``::group::Pre task execution`` / ``::group::Post task execution`` markers
# the Airflow 3.2 SPA's log viewer recognizes as drill-down sections. In
# practice the SPA renders our markers as <details open={hash-only}> elements
# that default to COLLAPSED (no hash → open=false) and the controlled-component
# toggle does not respond to clicks, so users could not expand the groups to
# see their print() / lifecycle output. The net effect was the OPPOSITE of the
# intent: lifecycle info was hidden by default with no reliable way to reveal
# it. Until the SPA-side toggle bug is addressed, we emit lifecycle lines flat
# — Airflow's standard "no groups → everything visible" layout. The Pre/Post
# drill-down can be reintroduced once the SPA renders them correctly.

DEFAULT_RETURN_VALUE_PATH = "/tmp/leoflow_return_value.json"  # noqa: S108


def _load_call_args() -> dict:
    """Decode LEOFLOW_CALL_ARGS_JSON, the compile-time TaskFlow literals (#115).

    The parser captures literal call args of a ``@task`` invocation
    (``shard(n=0)`` → ``{"n": 0}``) and the agent stamps the result as the
    LEOFLOW_CALL_ARGS_JSON env var. Malformed JSON is silently dropped: the
    parser's contract is to emit valid JSON, and dying with a JSON error the
    user did not write would be worse than running with the function's
    defaults. The env name is call_args (not params) to leave Airflow's
    DAG-run params term free for a future feature (#148).
    """
    raw = os.environ.get("LEOFLOW_CALL_ARGS_JSON", "")
    if not raw:
        return {}
    try:
        decoded = json.loads(raw)
    except (TypeError, ValueError):
        return {}
    return decoded if isinstance(decoded, dict) else {}


def _resolve_kwargs(fn) -> dict:
    """Resolve each of fn's parameters from compile-time literals and upstream XCom.

    Two injection paths are merged into the same kwargs map:

    - **LEOFLOW_CALL_ARGS_JSON** (#115): the literal args the user wrote at
      the ``@task`` call site (``shard(n=0)``), captured by the parser at
      compile time.
    - **LEOFLOW_XCOM_<PARAM>**: an upstream task's ``return_value``, fetched
      by the agent at dispatch time. Takes precedence over a same-name
      literal so an explicit upstream binding always wins (in practice
      ``shard(extract())`` would only have one or the other; the deterministic
      precedence keeps the contract clean).

    Parameters with neither binding are left unset so the function's defaults
    apply (or it raises TypeError if it has none — exactly Airflow's
    semantics).
    """
    call_args = _load_call_args()
    kwargs: dict = {}
    for name, param in inspect.signature(fn).parameters.items():
        if param.kind in (inspect.Parameter.VAR_POSITIONAL, inspect.Parameter.VAR_KEYWORD):
            continue
        if name in call_args:
            kwargs[name] = call_args[name]
        # Read the raw env var here (rather than via xcom_pull) so we can log
        # the wire size when the pull succeeds — invaluable when debugging a
        # task that "received None" because the upstream payload didn't arrive.
        # XCom takes precedence over the literal call arg of the same name
        # (matches the precedence comment above and prior behavior).
        raw = os.environ.get(XCOM_ENV_PREFIX + name.upper())
        if raw is not None:
            kwargs[name] = json.loads(raw)
            _lifecycle(f"pulled {name} ({len(raw)} B)")
    return kwargs


def return_value_path() -> str:
    """Return the path the task's return value is written to.

    Overridable via ``LEOFLOW_RETURN_VALUE_PATH`` (primarily for tests).
    """
    return os.environ.get("LEOFLOW_RETURN_VALUE_PATH", DEFAULT_RETURN_VALUE_PATH)


def run(entrypoint: str) -> None:
    """Import and call ``module:callable``, writing a non-None return as JSON.

    The agent reads the file and pushes it as the task's ``return_value`` XCom.
    A None return writes nothing, so downstream tasks see no XCom.

    Emits three lifecycle lines so the UI's log panel is informative even when
    the user function is silent: ``loading <entrypoint>``, ``resolved kwargs:
    {...}`` (when any), and ``returned <repr>`` (success) or no extra line on
    raise (the agent wraps the traceback). All three use ``[leoflow]`` prefix
    via :func:`_lifecycle` so they are distinguishable from user ``print()``.
    """
    module_name, sep, fn_name = entrypoint.partition(":")
    if not sep or not module_name or not fn_name:
        raise ValueError(f"entrypoint must be 'module:callable', got {entrypoint!r}")

    _lifecycle(f"loading {entrypoint}")
    module = importlib.import_module(module_name)
    fn = getattr(module, fn_name)
    # Airflow TaskFlow @task decorators are not executed when called directly —
    # calling them returns an XComArg (a task reference), not the function's
    # result. Unwrap to the underlying Python function so we run the user's code
    # and capture its real return value.
    if hasattr(fn, "function"):
        fn = fn.function
    kwargs = _resolve_kwargs(fn)
    if kwargs:
        # Log keys only — the values can carry XCom-pulled secrets or any
        # user payload; per ADR 0032 they belong in the XCom tab, not in the
        # log file. The pulled lines above already report each XCom source
        # and its wire size so the operator can correlate.
        _lifecycle(f"resolved kwargs: {sorted(kwargs.keys())}")

    try:
        result = fn(**kwargs)
    except Exception as exc:  # noqa: BLE001 — surface user errors to the log
        _lifecycle(f"user function {fn_name} raised {type(exc).__name__}: {exc}")
        # Stdout is line-buffered or `-u` unbuffered; flush stderr too so the
        # ordering in the log panel matches the wall-clock order.
        sys.stdout.flush()
        sys.stderr.flush()
        # Re-raise so Python's default handler emits the traceback to stderr —
        # the agent captures it.
        raise

    _write_return(result)


def _write_return(result) -> None:
    """Write a non-None return as JSON to the return-value file (the agent pushes
    it as the task's return_value XCom). A None return writes nothing."""
    if result is None:
        _lifecycle("returned None (no XCom pushed)")
        return
    try:
        payload = json.dumps(result)
    except (TypeError, ValueError):
        # Airflow operators can return non-JSON objects; stringify so the XCom is
        # at least usable rather than failing the task on serialization.
        payload = json.dumps(repr(result))
    _lifecycle(f"returned {type(result).__name__} ({len(payload)} B XCom)")
    with open(return_value_path(), "w", encoding="utf-8") as fh:
        fh.write(payload)


# Env var carrying the upstream task_id -> return_value map for ti.xcom_pull. It must
# NOT start with XCOM_ENV_PREFIX ("LEOFLOW_XCOM_"): _merge_operator_xcom consumes every
# such var as a param-bound operator kwarg, and would otherwise inject this whole map
# as a bogus `by_task=` kwarg (a real collision the live GCP chain test caught).
UPSTREAM_XCOM_ENV = "LEOFLOW_UPSTREAM_XCOM"


def _load_xcom_by_task() -> dict:
    """Decode UPSTREAM_XCOM_ENV — the map of upstream ``task_id`` to its decoded
    ``return_value`` the agent delivers so ``ti.xcom_pull`` can resolve it (chained
    operators). Malformed/absent → empty: a missing upstream pulls as None, never a
    crash."""
    raw = os.environ.get(UPSTREAM_XCOM_ENV)
    if not raw:
        return {}
    try:
        decoded = json.loads(raw)
    except (TypeError, ValueError):
        return {}
    return decoded if isinstance(decoded, dict) else {}


class _StandaloneTaskInstance:
    """A minimal, tolerant TaskInstance stand-in for a standalone operator run (ADR
    0040 Phase A).

    Almost every provider operator calls ``context["ti"].xcom_push(...)`` at the end
    of ``execute()`` to persist its UI operator link (see Airflow's
    ``.../links/base.py``); a few read ``try_number``. Leoflow propagates only the
    operator's ``execute()`` return value as the downstream XCom (see
    :func:`_write_return`), so ``xcom_push``/``xcom_pull`` are deliberate no-ops —
    they drop the UI link and any extra keyed XCom that Leoflow does not consume,
    rather than crashing the task *after* its real side effect (which on retry would
    re-run a non-idempotent operator). Identity fields come from the agent's env.
    Attributes beyond these are intentionally absent: an operator that needs the real
    metastore then fails loudly rather than running silently wrong.
    """

    def __init__(self) -> None:
        self.task_id = os.environ.get("LEOFLOW_TASK_ID", "")
        self.dag_id = os.environ.get("LEOFLOW_DAG_ID", "")
        self.run_id = os.environ.get("LEOFLOW_RUN_ID", "")
        self.map_index = -1
        try:
            self.try_number = int(os.environ.get("LEOFLOW_TRY_NUMBER", "1"))
        except (TypeError, ValueError):
            self.try_number = 1
        self._xcom_by_task = _load_xcom_by_task()

    def xcom_push(self, *args, **kwargs) -> None:
        """No-op: Leoflow propagates only the execute() return value (see
        :func:`_write_return`), not UI-link or extra keyed XCom pushes."""

    def xcom_pull(self, task_ids=None, dag_id=None, key="return_value", *args, **kwargs):
        """Resolve an upstream task's ``return_value`` — the Airflow-idiomatic way
        chained operators pass data (``ti.xcom_pull('compile')['name']``). The agent
        delivers each declared upstream's return_value in UPSTREAM_XCOM_ENV; only
        the ``return_value`` key is carried (custom keys → None, matching Leoflow's
        single-payload XCom model). ``task_ids`` may be a single id (→ one value) or a
        list (→ a list, like Airflow); an unknown id resolves to ``default``
        (Airflow's ``default`` kwarg, itself None unless the caller passes one).
        """
        default = kwargs.get("default")
        if key not in (None, "return_value"):
            return default
        if isinstance(task_ids, (list, tuple)):
            return [self._xcom_by_task.get(t, default) for t in task_ids]
        if task_ids is None:
            return default
        return self._xcom_by_task.get(task_ids, default)


def _operator_context() -> dict:
    """A minimal Airflow execution context for a standalone operator run (ADR 0040
    Phase A). The agent supplies the run's macros via env; many operators read no
    context at all, so sensible defaults are enough for the common case. ``ti`` /
    ``task_instance`` are a tolerant :class:`_StandaloneTaskInstance` (not None) so
    the near-universal operator-link ``ti.xcom_push`` pattern does not crash."""
    params = {}
    raw_params = os.environ.get("LEOFLOW_PARAMS")
    if raw_params:
        try:
            params = json.loads(raw_params)
        except (TypeError, ValueError):
            params = {}
    ds = os.environ.get("LEOFLOW_DS", "")
    ti = _StandaloneTaskInstance()
    return {
        "ds": ds,
        "ts": os.environ.get("LEOFLOW_TS", ""),
        "run_id": os.environ.get("LEOFLOW_RUN_ID", ""),
        "params": params,
        "task_instance": ti,
        "ti": ti,
        "dag_run": None,
    }


def _merge_operator_xcom(args: dict) -> dict:
    """Inject upstream XCom values into a captured operator's kwargs (ADR 0040
    A1.1). The parser records an operator arg bound to an upstream task's output
    as an xcom_input; the agent fetches that upstream's return_value and delivers
    it as ``LEOFLOW_XCOM_<PARAM>``. Here we decode each and set it on the matching
    kwarg, so ``MyOperator(sql=extract())`` runs with the real upstream SQL. An
    XCom value overrides any same-name literal, matching the @task precedence.
    """
    for key, raw in os.environ.items():
        if not key.startswith(XCOM_ENV_PREFIX):
            continue
        param = key[len(XCOM_ENV_PREFIX):].lower()
        args[param] = json.loads(raw)
        _lifecycle(f"pulled {param} for operator ({len(raw)} B)")
    return args


def run_operator(operator_class: str, args: dict) -> None:
    """Instantiate and execute a captured Airflow operator/sensor (ADR 0040 Phase
    A): ``import_string(class)(task_id, **args) → render_template_fields → execute``.
    The provider is installed in the task image (declared via connectors:/
    dependencies:); connections resolve from AIRFLOW_CONN_* exactly as a hook's do.
    """
    _lifecycle(f"loading operator {operator_class}")
    module_name, _, class_name = operator_class.rpartition(".")
    if not module_name:
        raise ValueError(f"operator class must be dotted, got {operator_class!r}")
    op_cls = getattr(importlib.import_module(module_name), class_name)
    task_id = os.environ.get("LEOFLOW_TASK_ID", class_name)
    op = op_cls(task_id=task_id, **_merge_operator_xcom(dict(args)))

    # Reject a reschedule-mode sensor up front (review polish #5). Its execute()
    # reaches for a real TaskInstance (ti.get_first_reschedule_date) that our
    # minimal standalone context cannot provide, so it would otherwise crash with
    # a confusing AttributeError rather than raising AirflowRescheduleException.
    if getattr(op, "mode", None) == "reschedule":
        raise RuntimeError(
            f"{class_name} is a reschedule-mode sensor, which Leoflow does not "
            f"support yet (ADR 0040 Phase B). Set mode='poke' — the sensor holds "
            f"the worker until its condition is met.")

    context = _operator_context()
    try:
        op.render_template_fields(context)
    except Exception as exc:  # noqa: BLE001 — templating is best-effort in Phase A
        _lifecycle(f"render_template_fields skipped ({type(exc).__name__}: {exc})")

    _lifecycle(f"executing {class_name}.execute()")
    try:
        result = op.execute(context)
    except Exception as exc:  # noqa: BLE001 — translate reschedule/deferral; re-raise the rest
        if _is_reschedule_exc(exc):
            raise RuntimeError(
                f"{class_name} asked to reschedule (sensor mode='reschedule'), which "
                f"Leoflow does not support yet (ADR 0040 Phase B). Use mode='poke' — "
                f"the sensor holds the worker until its condition is met.") from exc
        if _is_deferral_exc(exc):
            raise RuntimeError(
                f"{class_name} asked to defer (deferrable=True), which Leoflow does not "
                f"support yet (ADR 0040 Phase C — no triggerer). Pass deferrable=False — "
                f"the operator runs synchronously in the pod (poke-style).") from exc
        raise
    _write_return(result)


def _is_reschedule_exc(exc: BaseException) -> bool:
    """True if exc is Airflow's AirflowRescheduleException (matched by name across
    the MRO, so the runtime needn't import Airflow to recognize it). Reschedule
    mode is ADR 0040 Phase B; until then we translate it to a clear error instead
    of leaking a raw traceback (review polish #5)."""
    return any(base.__name__ == "AirflowRescheduleException" for base in type(exc).__mro__)


def _is_deferral_exc(exc: BaseException) -> bool:
    """True if exc is Airflow's TaskDeferred (matched by name across the MRO, so the
    runtime needn't import Airflow). A deferrable operator (deferrable=True) raises
    it to suspend onto a trigger; Leoflow has no triggerer yet (ADR 0040 Phase C),
    so we translate it into a clear "set deferrable=False" error rather than leaking
    a raw TaskDeferred traceback."""
    return any(base.__name__ == "TaskDeferred" for base in type(exc).__mro__)
