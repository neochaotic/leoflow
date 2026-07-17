"""Compile an Airflow DAG into the canonical Leoflow dag.json.

The compiler imports the DAG module through Airflow's DagBag (which never runs
task bodies), inspects ``dag.task_dict``, and maps each operator to a Leoflow
task. It supports Python (including TaskFlow ``@task``), Bash, and HTTP tasks.
"""
from __future__ import annotations

import inspect
import json
import os
import runpy
import sys
import warnings
from pathlib import Path
from typing import Any

# Per-project config arrives from the Go CLI via this env var. The CLI parses
# leoflow.yaml with gopkg.in/yaml.v3 and hands the resolved (defaults
# applied) struct here as JSON — so the parser has zero third-party deps and
# the Go side stays the single source of truth for the config schema.
_CONFIG_ENV = "LEOFLOW_PROJECT_CONFIG_JSON"

_SUPPORTED_TRIGGER_RULES = {
    "all_success",
    "all_failed",
    "all_done",
    "one_success",
    "one_failed",
}


def compile_dag(
    source: str,
    config_path: str,
    image: str,
    dag_version: str = "dev",
) -> dict[str, Any]:
    """Compile the DAG in ``source`` into a dag.json dictionary."""
    config = _load_config(config_path)
    dag = _load_dag(source, config.get("dag_id"))

    spec: dict[str, Any] = {
        "schema_version": "1.0",
        "dag_id": dag.dag_id,
        "dag_version": dag_version,
        "image": image,
        "tasks": [_map_task(task, source) for task in _ordered_tasks(dag)],
    }

    schedule = _schedule(dag)
    if schedule is not None:
        spec["schedule"] = schedule
    if config.get("owner"):
        spec["owner"] = config["owner"]
    tags = config.get("tags") or sorted(getattr(dag, "tags", []) or [])
    if tags:
        spec["tags"] = list(tags)
    default_args = _default_args(config)
    if default_args:
        spec["default_args"] = default_args
    return spec


def _load_config(path: str) -> dict[str, Any]:
    """Resolve the project config, preferring the Go-marshalled JSON in
    ``LEOFLOW_PROJECT_CONFIG_JSON`` (production path: the CLI parses the
    YAML and hands us the result). Falls back to reading ``path`` as JSON
    when the env var is unset — kept for direct test invocation. We do NOT
    fall back to YAML on disk: PyYAML was vendored solely to bridge this
    handshake and was removed at the alpha cut; pointing at a ``.yaml``
    path without the env var yields a clear actionable error rather than
    a silent ImportError."""
    raw = os.environ.get(_CONFIG_ENV)
    if raw is not None:
        if raw.strip() == "":
            return {}
        try:
            data = json.loads(raw)
        except json.JSONDecodeError as exc:  # pragma: no cover - operator error
            raise ValueError(
                f"{_CONFIG_ENV} is set but does not contain valid JSON: {exc}"
            ) from exc
        return data or {}
    if not path:
        raise ValueError(
            f"no project config available: set {_CONFIG_ENV} (the Leoflow CLI "
            "does this automatically) or pass a JSON config file path"
        )
    if not os.path.isfile(path):
        raise ValueError(
            f"no project config available: {_CONFIG_ENV} is unset and "
            f"{path!r} does not exist. The Leoflow CLI normally sets the "
            "env var; if you are invoking the parser directly, set it or "
            "pass a path to a JSON config file."
        )
    suffix = os.path.splitext(path)[1].lower()
    if suffix in (".yaml", ".yml"):
        raise ValueError(
            f"YAML config files are no longer parsed in-process (PyYAML was "
            f"removed at the alpha cut). Set {_CONFIG_ENV} to the JSON "
            f"output of `leoflow compile` config resolution, or convert "
            f"{path!r} to JSON."
        )
    with open(path, encoding="utf-8") as handle:
        return json.loads(handle.read() or "{}") or {}


def _load_dag(source: str, dag_id: str | None):
    """Load the DAG(s) from ``source`` and select one. The default backend is the
    dependency-free structural shim (ADR 0024); set LEOFLOW_PARSER_BACKEND=airflow
    to use the real Airflow DagBag (requires apache-airflow installed)."""
    if os.environ.get("LEOFLOW_PARSER_BACKEND") == "airflow":
        dags, error = _load_dags_airflow(source)
    else:
        dags, error = _load_dags_shim(source)

    if error:
        raise ValueError(f"failed to import {source}: {error}")
    if not dags:
        raise ValueError(f"no DAG found in {source}")
    if dag_id and dag_id in dags:
        return dags[dag_id]
    if dag_id:
        raise ValueError(f"DAG {dag_id!r} not found in {source}; found {sorted(dags)}")
    if len(dags) > 1:
        raise ValueError(f"multiple DAGs in {source}; set dag_id in leoflow.yaml")
    return next(iter(dags.values()))


def _ensure_shim_on_path() -> None:
    """Put the bundled `airflow` shim first on sys.path so the user's DAG imports
    resolve to it, dropping any real-airflow modules already imported in-process."""
    shim_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "_shim")
    try:
        sys.path.remove(shim_dir)
    except ValueError:
        pass
    sys.path.insert(0, shim_dir)
    for name in list(sys.modules):
        if (name == "airflow" or name.startswith("airflow.")) and \
                shim_dir not in (getattr(sys.modules[name], "__file__", "") or ""):
            del sys.modules[name]


def _load_dags_shim(source: str):
    """Exec the DAG file against the structural shim; return ({dag_id: DAG}, error).
    An unsupported operator surfaces as a clear import error (ADR 0024)."""
    _ensure_shim_on_path()
    import airflow._core as core  # the shim

    core.reset()
    # Put the DAG's own directory on sys.path so sibling-module imports
    # (`from helpers import ...` next to dag.py) resolve, as Airflow's DagBag does.
    source_dir = os.path.dirname(os.path.abspath(source))
    added_dir = source_dir not in sys.path
    if added_dir:
        sys.path.insert(0, source_dir)
    before = set(sys.modules)
    try:
        runpy.run_path(source, run_name="__leoflow_dag__")
    except ModuleNotFoundError as exc:
        if _is_provider_module(exc.name):
            return {}, _provider_import_hint(exc.name)
        return {}, _unsupported(f"module {exc.name!r}")
    except ImportError as exc:
        # A missing name the shim does not provide (e.g. `chain`, a Branch operator).
        return {}, _unsupported(str(exc))
    except NotImplementedError as exc:
        # A construct the shim deliberately rejects (e.g. dynamic task mapping).
        return {}, _unsupported(str(exc))
    finally:
        # Isolate compiles: drop modules the DAG imported (incl. sibling helpers)
        # and remove its directory, so repeated compiles never serve stale code.
        for name in set(sys.modules) - before:
            del sys.modules[name]
        if added_dir:
            try:
                sys.path.remove(source_dir)
            except ValueError:
                pass
    return dict(core.COLLECTED), None


def _unsupported(detail: str) -> str:
    return (f"{detail}: not supported by Leoflow "
            f"(supported: Bash, Http, Python/@task; no dynamic task mapping or task groups)")


def _is_provider_module(name: str | None) -> bool:
    """True for an Airflow provider module (`airflow.providers.<x>`), except the
    bundled `standard` provider — which the shim supplies, so a failure there is
    a real bug, not a missing-dependency the connectors: hint would address."""
    if not name:
        return False
    return name.startswith("airflow.providers.") and not name.startswith(
        "airflow.providers.standard"
    )


def _provider_import_hint(name: str) -> str:
    """Actionable message for a provider hook/operator imported at the DAG module
    top level. Leoflow parses DAGs without providers installed, so the import
    fails here even when the provider IS declared — the fix is to import it inside
    the @task body (which the parser never executes) and declare it so it lands in
    the task runtime. ADR 0038 #2.

    `name` is the failed module, e.g. 'airflow.providers.postgres'. We do not
    translate it to a connector short name here (that mapping is the Go catalog's
    job); we point at the mechanism so the message stays correct for every
    provider, curated or not."""
    return (
        f"{name!r} is an Airflow provider imported at the DAG module top level, "
        f"which Leoflow cannot resolve while parsing (providers are not installed "
        f"in the parser). Import the hook/operator INSIDE your @task function, and "
        f"declare the provider in leoflow.yaml via `connectors:` (short names like "
        f"postgres, http) or `dependencies:` (an explicit pip package) so it is "
        f"installed in the task runtime."
    )


def _load_dags_airflow(source: str):
    """Fallback loader using the real Airflow DagBag (opt-in)."""
    from airflow.dag_processing.dagbag import DagBag

    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        bag = DagBag(dag_folder=source, include_examples=False)
    error = f"{bag.import_errors}" if bag.import_errors else None
    return dict(bag.dags), error


def _ordered_tasks(dag) -> list[Any]:
    return [dag.task_dict[task_id] for task_id in sorted(dag.task_dict)]


def _map_task(task, source: str) -> dict[str, Any]:
    task_type = _operator_type(task)
    entry: dict[str, Any] = {"task_id": task.task_id, "type": task_type}

    upstream = sorted(task.upstream_task_ids)
    if upstream:
        entry["depends_on"] = upstream

    rule = _trigger_rule(task)
    if rule != "all_success":
        entry["trigger_rule"] = rule

    if task_type == "python":
        entry["entrypoint"] = _python_entrypoint(task, source)
        xcom_input, call_args = _bind_call_arguments(task)
        if xcom_input:
            entry["xcom_input"] = xcom_input
        if call_args:
            entry["call_args"] = call_args
    elif task_type == "bash":
        entry["entrypoint"] = _bash_command(task)
    elif task_type == "http_api":
        entry["http_request"] = _http_request(task)
    elif task_type == "airflow_operator":
        entry["operator_class"] = type(task).__leoflow_operator_class__
        xcom_input, operator_args = _split_operator_args(task)
        if xcom_input:
            entry["xcom_input"] = xcom_input
        if operator_args:
            entry["operator_args"] = operator_args
    _check_callbacks(task, task_type, entry)
    return entry


# _ON_FAILURE_CALLBACK is the one Airflow callback kwarg Leoflow runs (#424); the
# others stay a loud reject until wired.
_ON_FAILURE_CALLBACK = "on_failure_callback"

# Task types whose runtime path is Python with a real try/except, so an
# on_failure_callback can run in-process on the task's final failure (#424). A
# bash task execs bash in place (os.execvp), leaving no Python to run it.
_CALLBACK_CAPABLE_TYPES = ("airflow_operator", "python")


def _has_callback(task, name: str) -> bool:
    """Report whether the task declares a callable ``name``. Native tasks store it
    as an attribute (the shim setattrs every kwarg); a generically-captured operator
    carries it in ``__leoflow_args__``. Check both."""
    if callable(getattr(task, name, None)):
        return True
    return callable((getattr(task, "__leoflow_args__", {}) or {}).get(name))


def _check_callbacks(task, task_type: str, entry: dict) -> None:
    """Resolve Airflow lifecycle callbacks (#424). ``on_failure_callback`` is marked
    on a Python-path task (operator or @task) so the runtime runs it; on a bash task
    it is a loud reject (no Python is left to run it). ``on_success_callback`` /
    ``on_retry_callback`` are unsupported everywhere and always a loud reject —
    never a silent drop, which would leave the author thinking their callback runs."""
    for name in ("on_success_callback", "on_retry_callback"):
        if _has_callback(task, name):
            raise ValueError(
                f"{name} on task {task.task_id!r} is not supported by Leoflow yet — "
                "refusing to silently drop it. Use an alerts: block in leoflow.yaml, "
                "or a downstream @task with a trigger_rule.")
    if not _has_callback(task, _ON_FAILURE_CALLBACK):
        return
    if task_type in _CALLBACK_CAPABLE_TYPES:
        entry[_ON_FAILURE_CALLBACK] = True
        return
    raise ValueError(
        f"on_failure_callback on task {task.task_id!r} (type {task_type}) cannot run: "
        "a bash task replaces its process with bash, so no Python is left to call it. "
        "Use an alerts: block in leoflow.yaml, or a downstream @task with "
        "trigger_rule='one_failed'.")


def _split_operator_args(task) -> tuple[dict[str, list[str]], dict[str, Any]]:
    """Split a captured operator's constructor kwargs into XCom inputs and JSON
    literals (ADR 0040 A1.1).

    - An arg bound to an upstream task's output (``MyOperator(sql=extract())``)
      becomes an **xcom_input** — ``arg → [upstream_task_ids…]`` — so the agent
      fetches the upstream's return_value and the runtime injects it. A list of
      outputs is fan-in, mirroring the @task path (:func:`_bind_call_arguments`).
    - A JSON-serialisable literal becomes an **operator_arg**, carried verbatim
      in dag.json and delivered via LEOFLOW_OPERATOR_ARGS.

    An arg that is neither — a callable, a datetime, an arbitrary object — is a
    **loud compile error**, not a silent drop: the generic executor cannot carry
    it across dag.json, and dropping it would run the operator wrong. Move that
    logic into a @task, or pass a JSON-serialisable value / an upstream output.
    """
    raw = getattr(task, "__leoflow_args__", {}) or {}
    xcom: dict[str, list[str]] = {}
    operator_args: dict[str, Any] = {}
    for name, value in raw.items():
        # on_failure_callback is a callable that cannot be serialised into
        # dag.json, but it is SUPPORTED (#424 inc 4): the runtime re-imports dag.py
        # and calls it in the task process on failure. Accept it here as a marker
        # (see _has_on_failure_callback) instead of the non-serialisable reject.
        if name == _ON_FAILURE_CALLBACK and callable(value):
            continue
        single_upstream = getattr(getattr(value, "operator", None), "task_id", None)
        if single_upstream:
            xcom[name] = [single_upstream]
            continue
        if isinstance(value, (list, tuple)) and value and all(
                getattr(getattr(item, "operator", None), "task_id", None)
                for item in value):
            xcom[name] = [item.operator.task_id for item in value]
            continue
        if _is_json_literal(value):
            operator_args[name] = value
            continue
        raise ValueError(
            f"operator argument {name!r} on task {task.task_id} is a "
            f"{type(value).__name__}, which Leoflow cannot carry in dag.json; "
            f"pass a JSON-serialisable value or an upstream task's output, or "
            f"move the logic into a @task")
    return xcom, operator_args


def _operator_type(task) -> str:
    name = type(task).__name__
    # A dbt group placeholder (ADR 0043): the Go compiler expands it into one task
    # per dbt node after parsing. It carries no operator semantics here.
    if getattr(type(task), "__leoflow_dbt_group__", False):
        return "dbt_group"
    # Reject silent-wrong-execution shapes BEFORE the substring catch-alls below
    # (issue #225). BranchPythonOperator / ShortCircuitOperator / @task.branch
    # all carry "Python" in their class name, so the legacy substring match used
    # to translate them to a plain `python` task — and every "skipped" branch
    # would then actually execute. Refusing them at compile is the loud failure
    # the parser owes; remove the gate when real translation lands.
    # Control-flow operators need scheduler branch/skip support we do not have yet
    # (ADR 0040 Phase D); refuse rather than mistranslate. Provider operators and
    # sensors ARE captured generically below.
    for marker, feature in (
        ("Branch", "branching (@task.branch / BranchPythonOperator)"),
        ("ShortCircuit", "short-circuit (@task.short_circuit / ShortCircuitOperator)"),
    ):
        if marker in name:
            raise ValueError(
                f"unsupported operator {name!r} on task {task.task_id!r}: "
                f"{feature} is not supported by Leoflow yet — refusing to "
                "silently mistranslate. See docs/dag-authoring.md for the "
                "current supported operator list."
            )
    # Any captured provider operator / sensor / transfer runs through the generic
    # executor (ADR 0040 Phase A): import_string(class)(**args).execute(context). Only
    # _generic-captured classes carry __leoflow_operator_class__; the bundled shim
    # operators (Bash/Python/Http) do not. This check MUST precede the substring
    # fast-path below: a long-tail operator whose class name happens to contain
    # Bash/Http/Python (e.g. AcmePythonModelOperator) would otherwise be mistranslated
    # into a native task, silently dropping its operator_class — the same
    # silent-mistranslation class as the HttpSensor regression guarded below.
    if getattr(type(task), "__leoflow_operator_class__", None):
        return "airflow_operator"
    # The native fast path is for the BUNDLED shim operators only (which carry no
    # __leoflow_operator_class__, so they fall through to here). A SENSOR whose class
    # name happens to contain Bash/Http/Python (HttpSensor, BashSensor, PythonSensor)
    # must NOT be mistranslated into a native one-shot task — sensors run via the
    # generic poke executor in their own pod. (An e2e caught HttpSensor silently
    # becoming an inline http_api call.) Airflow sensors conventionally end in "Sensor".
    if not name.endswith("Sensor"):
        if "Bash" in name:
            return "bash"
        if "Http" in name:
            return "http_api"
        if "Python" in name:
            return "python"
    raise ValueError(f"unsupported operator {name!r} on task {task.task_id}")


def _trigger_rule(task) -> str:
    value = getattr(task.trigger_rule, "value", str(task.trigger_rule))
    if value not in _SUPPORTED_TRIGGER_RULES:
        raise ValueError(f"unsupported trigger rule {value!r} on task {task.task_id}")
    return value


def _python_entrypoint(task, source: str) -> str:
    callable_obj = getattr(task, "python_callable", None)
    name = getattr(callable_obj, "__name__", task.task_id)
    return f"{Path(source).stem}:{name}"


def _bind_call_arguments(task) -> tuple[dict[str, list[str]], dict[str, Any]]:
    """Split a TaskFlow task's bound arguments into XCom inputs and literal call args.

    For ``transform(extract())`` the operator stores ``extract``'s XComArg as
    one argument; for ``shard(0)`` it stores the literal ``0``; for
    ``combine([a(), b(), c()])`` it stores a list of XComArgs (fan-in).
    Binding all of them against the callable's signature lets us split by type:

    - **xcom_input**: ``param_name → [upstream_task_ids…]``. ALWAYS a list,
      even for a single upstream (uniform schema). The agent fetches every
      upstream's ``return_value`` at dispatch time; for fan-in (len > 1)
      the runtime receives a JSON array, so the function parameter is the
      list of upstream values, in declaration order.
    - **call_args** (#115): ``param_name → JSON-serialisable literal``. The
      agent stamps the whole map as LEOFLOW_CALL_ARGS_JSON; the runtime
      decodes and delivers it to the function. Named call_args (not params)
      to keep Airflow's DAG-run params term free (#148).

    Arguments that are neither (e.g. a non-serialisable Python object) are
    silently dropped so the function falls back to its default — matching the
    pre-existing tolerance of the parser. An XComArg always wins precedence
    via the runtime layer, where the merge happens deterministically.
    """
    callable_obj = getattr(task, "python_callable", None)
    if callable_obj is None:
        return {}, {}
    op_args = getattr(task, "op_args", ()) or ()
    op_kwargs = getattr(task, "op_kwargs", {}) or {}
    try:
        bound = inspect.signature(callable_obj).bind_partial(*op_args, **op_kwargs)
    except (TypeError, ValueError):
        return {}, {}
    xcom: dict[str, list[str]] = {}
    call_args: dict[str, Any] = {}
    for name, value in bound.arguments.items():
        operator = getattr(value, "operator", None)
        single_upstream = getattr(operator, "task_id", None)
        if single_upstream:
            xcom[name] = [single_upstream]
            continue
        # Fan-in: `combine([a(), b(), c()])` binds a list of XComArgs to one
        # parameter. The agent fetches each upstream and the runtime receives
        # them as a JSON array (in declaration order). A mixed list with any
        # non-XComArg item is ambiguous (literal vs. upstream) — fall through
        # to the literal path so json.dumps rejects it cleanly.
        if isinstance(value, (list, tuple)) and value and all(
                getattr(getattr(item, "operator", None), "task_id", None)
                for item in value):
            xcom[name] = [item.operator.task_id for item in value]
            continue
        if _is_json_literal(value):
            call_args[name] = value
    return xcom, call_args


def _is_json_literal(value: Any) -> bool:
    """Reports whether value is safely round-trippable through JSON.

    The runtime delivers params via LEOFLOW_PARAMS_JSON, so a value that
    survives ``json.dumps`` cleanly is the safe-to-capture set. Anything
    else (a class instance, a tuple of objects, a function) is dropped so
    we never emit invalid JSON into dag.json.
    """
    try:
        json.dumps(value)
    except (TypeError, ValueError):
        return False
    return True


def _bash_command(task) -> str:
    return getattr(task, "bash_command", "") or ""


def _http_request(task) -> dict[str, Any]:
    request: dict[str, Any] = {
        "method": (getattr(task, "method", "GET") or "GET").upper(),
        "url": getattr(task, "endpoint", "") or "",
    }
    headers = getattr(task, "headers", None)
    if headers:
        request["headers"] = dict(headers)
    body = getattr(task, "data", None)
    if body:
        request["body"] = body
    return request


def _schedule(dag) -> str | None:
    timetable = getattr(dag, "timetable", None)
    summary = getattr(timetable, "summary", None)
    if isinstance(summary, str) and summary:
        return summary
    schedule = getattr(dag, "schedule", None)
    return schedule if isinstance(schedule, str) else None


def _default_args(config: dict[str, Any]) -> dict[str, Any]:
    defaults = config.get("defaults") or {}
    out: dict[str, Any] = {}
    for key in ("retries", "retry_delay_seconds", "execution_timeout_seconds"):
        if key in defaults:
            out[key] = defaults[key]
    return out
