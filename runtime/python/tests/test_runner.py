"""Tests for the Leoflow task runner."""

import itertools
import json

import pytest

from leoflow_runtime import runner

_counter = itertools.count()


def _write_module(tmp_path, monkeypatch, body: str) -> str:
    """Write a uniquely-named module so each test imports fresh code."""
    name = f"usermod_{next(_counter)}"
    (tmp_path / f"{name}.py").write_text(body)
    monkeypatch.syspath_prepend(str(tmp_path))
    return name


def test_run_writes_return_value(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_module(tmp_path, monkeypatch, "def task():\n    return {'rows': 7}\n")

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == {"rows": 7}


def test_run_injects_context_into_named_params(tmp_path, monkeypatch):
    # A @task gets the run context the same way a captured operator does: params named
    # after context keys are injected (def task(ds=None)), so native tasks are on par.
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_DS", "2026-06-14")
    monkeypatch.setenv("LEOFLOW_RUN_ID", "run-9")
    body = "def task(ds=None, run_id=None):\n    return {'ds': ds, 'run_id': run_id}\n"
    mod = _write_module(tmp_path, monkeypatch, body)

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == {"ds": "2026-06-14", "run_id": "run-9"}


def test_run_injects_full_context_into_kwargs(tmp_path, monkeypatch):
    # def task(**context): gets the whole context (the documented "old style").
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_DS", "2026-06-14")
    monkeypatch.setenv("LEOFLOW_PARAMS", '{"region": "us"}')
    body = ("def task(**context):\n"
            "    return {'ds': context['ds'], 'region': context['params']['region']}\n")
    mod = _write_module(tmp_path, monkeypatch, body)

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == {"ds": "2026-06-14", "region": "us"}


def test_run_explicit_binding_overrides_context(tmp_path, monkeypatch):
    # call_args / XCom win over context — existing behavior preserved, nothing broken.
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_DS", "ctx-ds")
    monkeypatch.setenv("LEOFLOW_CALL_ARGS_JSON", '{"ds": "explicit-ds"}')
    mod = _write_module(tmp_path, monkeypatch, "def task(ds=None):\n    return {'ds': ds}\n")

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == {"ds": "explicit-ds"}


def test_run_without_return_writes_no_file(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_module(tmp_path, monkeypatch, "def task():\n    return None\n")

    runner.run(f"{mod}:task")

    assert not out.exists()


def test_run_rejects_bad_entrypoint():
    with pytest.raises(ValueError):
        runner.run("no_callable_here")


def test_run_lifecycle_never_logs_kwargs_values(tmp_path, monkeypatch, capsys):
    """ADR 0032: the runtime MUST NOT dump kwargs values to stdout — only the
    keys. Values can carry XCom-pulled secrets or any user payload; they
    belong in the XCom tab, not in the log file. Regression guard for the Lima
    leak found 2026-06-01 where `resolved kwargs: {'raw': 'SECRET'}` was
    bleeding the pulled XCom value straight into the log.
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    secret = "UPSTREAM_SECRET_VALUE_42"  # noqa: S105 — synthetic test marker, not a real secret
    monkeypatch.setenv("LEOFLOW_XCOM_RAW", f'"{secret}"')
    mod = _write_module(tmp_path, monkeypatch, (
        "def consumer(raw):\n"
        "    return len(raw)\n"
    ))

    runner.run(f"{mod}:consumer")
    stdout = capsys.readouterr().out

    # The kwargs lifecycle line MUST list keys but NEVER include the value.
    assert secret not in stdout, (
        f"ADR 0032 violation: pulled XCom value leaked into stdout:\n{stdout}"
    )
    assert "resolved kwargs: ['raw']" in stdout, (
        f"expected the keys-only kwargs line in:\n{stdout}"
    )
    # The pulled metadata line is still expected (size only).
    assert "[leoflow] pulled raw" in stdout
    assert "(26 B)" in stdout  # the JSON-encoded secret has 26 bytes


def test_run_logs_xcom_pulls_with_size(tmp_path, monkeypatch, capsys):
    """When an upstream XCom is consumed, the runtime MUST log it with the wire
    size — invaluable when debugging "received None" in a downstream task
    (was the upstream silent? did the name match? how big is the payload?).
    The line goes inside the Pre task execution group with the [leoflow]
    prefix.
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    # Two upstreams the function accepts as kwargs; agent would inject these.
    monkeypatch.setenv("LEOFLOW_XCOM_RAW", '{"rows":3}')
    monkeypatch.setenv("LEOFLOW_XCOM_SUMMARY", '"ok"')
    mod = _write_module(tmp_path, monkeypatch, (
        "def task(raw, summary):\n"
        "    return {'r': raw, 's': summary}\n"
    ))

    runner.run(f"{mod}:task")

    stdout = capsys.readouterr().out
    # Each pulled XCom gets its own [leoflow] pulled line with byte size.
    assert "[leoflow] pulled raw (10 B)" in stdout, f"missing raw pull line:\n{stdout}"
    assert "[leoflow] pulled summary (4 B)" in stdout, f"missing summary pull line:\n{stdout}"


def test_run_emits_flat_lifecycle_lines(tmp_path, monkeypatch, capsys):
    """The runtime MUST emit lifecycle lines as plain log lines, NOT wrapped in
    ``::group::``/``::endgroup::`` markers.

    The Airflow 3.2 SPA recognizes the markers but renders them as
    <details open={hash-only}> elements whose controlled-component toggle does
    not respond to clicks — so wrapping our lifecycle in groups made them
    invisible by default with no reliable way to expand. This test pins the
    flat layout (loading / kwargs / returned all visible by default) until the
    SPA-side toggle bug is addressed.
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_module(tmp_path, monkeypatch, (
        "def task():\n"
        "    print('USER_LINE_INSIDE_TASK')\n"
        "    return {'rows': 7}\n"
    ))

    runner.run(f"{mod}:task")

    stdout = capsys.readouterr().out
    # No drill-down markers should be emitted.
    assert "::group::" not in stdout, f"::group:: marker leaked into stdout:\n{stdout}"
    assert "::endgroup::" not in stdout, f"::endgroup:: marker leaked into stdout:\n{stdout}"
    # All three lifecycle lines and the user print MUST be present, in order.
    loading = stdout.find("[leoflow] loading")
    user_line = stdout.find("USER_LINE_INSIDE_TASK")
    returned = stdout.find("[leoflow] returned")
    assert loading != -1, f"missing loading line in:\n{stdout}"
    assert user_line != -1, f"missing user print in:\n{stdout}"
    assert returned != -1, f"missing returned line in:\n{stdout}"
    assert loading < user_line < returned, (
        f"ordering wrong: loading at {loading}, user at {user_line}, "
        f"returned at {returned} in:\n{stdout}"
    )


def test_run_propagates_user_exception(tmp_path, monkeypatch):
    mod = _write_module(tmp_path, monkeypatch, "def task():\n    raise RuntimeError('boom')\n")
    with pytest.raises(RuntimeError, match="boom"):
        runner.run(f"{mod}:task")


def test_run_unwraps_taskflow_decorator(tmp_path, monkeypatch):
    # Airflow TaskFlow @task objects return an XComArg (not the result) when
    # called bare; the runner must unwrap to .function and run the real code.
    # Regression guard for the pod-path bug where the task wrote a non-JSON
    # XComArg and exited 1.
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    body = (
        "class _XComArg:\n"
        "    pass\n"
        "class _TaskDecorator:\n"
        "    def __call__(self):\n"
        "        return _XComArg()  # not JSON serializable, mimics TaskFlow\n"
        "    def function(self):\n"
        "        return {'ran': True}\n"
        "task = _TaskDecorator()\n"
    )
    mod = _write_module(tmp_path, monkeypatch, body)

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == {"ran": True}


def test_run_resolves_xcom_input_arguments(tmp_path, monkeypatch):
    """A task consuming an upstream output receives it via LEOFLOW_XCOM_<param>."""
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    # the agent injects extract's return_value as the 'n' input
    monkeypatch.setenv("LEOFLOW_XCOM_N", "21")
    mod = _write_module(tmp_path, monkeypatch, "def transform(n):\n    return n * 2\n")

    runner.run(f"{mod}:transform")

    assert json.loads(out.read_text()) == 42


def test_run_leaves_unbound_params_to_defaults(tmp_path, monkeypatch):
    """A parameter with no injected XCom falls back to its default (no crash)."""
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_module(tmp_path, monkeypatch, "def task(x=5):\n    return x + 1\n")

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == 6


def test_run_resolves_literal_call_args_from_json(tmp_path, monkeypatch):
    """TaskFlow literal args (#115): @task f(5) captured at compile, delivered at run.

    The agent stamps LEOFLOW_CALL_ARGS_JSON with the literals captured by the
    parser. The runtime decodes and merges them into kwargs, so a
    ``shard(n=0)`` invocation at DAG-build time delivers ``n=0`` at execution.
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_CALL_ARGS_JSON", json.dumps({"n": 7}))
    mod = _write_module(tmp_path, monkeypatch, "def shard(n):\n    return n * 3\n")

    runner.run(f"{mod}:shard")

    assert json.loads(out.read_text()) == 21


def test_run_literal_call_args_carry_complex_values(tmp_path, monkeypatch):
    """Nested JSON literals (dicts, lists, None) round-trip through CALL_ARGS_JSON."""
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    payload = {"opts": {"shards": [1, 2, 3], "name": "demo"}, "limit": None}
    monkeypatch.setenv("LEOFLOW_CALL_ARGS_JSON", json.dumps(payload))
    mod = _write_module(
        tmp_path, monkeypatch,
        "def task(opts, limit):\n    return [opts['shards'], opts['name'], limit]\n",
    )

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == [[1, 2, 3], "demo", None]


def test_run_xcom_wins_over_literal_call_arg(tmp_path, monkeypatch):
    """XCom precedence: an upstream output supersedes a literal of the same name.

    Matches Airflow semantics — when a parameter is bound to both an upstream
    XComArg and a literal, the XComArg wins at runtime (the literal is only a
    compile-time placeholder, in practice you would never bind both, but the
    contract has to be deterministic).
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_CALL_ARGS_JSON", json.dumps({"n": 1}))
    monkeypatch.setenv("LEOFLOW_XCOM_N", "100")
    mod = _write_module(tmp_path, monkeypatch, "def task(n):\n    return n\n")

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == 100


def test_run_ignores_malformed_call_args_json(tmp_path, monkeypatch):
    """Malformed CALL_ARGS_JSON does not crash the runtime; silently dropped.

    The parser's contract is to emit valid JSON; if the env is malformed, the
    task is better off running with no literals (it may still find defaults
    or XCom) than dying with a JSON error the user never wrote.
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_CALL_ARGS_JSON", "{not valid json")
    mod = _write_module(tmp_path, monkeypatch, "def task(x=5):\n    return x\n")

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == 5


def test_run_operator_executes_captured_provider_operator(tmp_path, monkeypatch):
    """ADR 0040 A3: run_operator imports a captured operator and executes it,
    writing its return as the XCom. Skips when Airflow is not installed (the lean
    runtime test env); runs against the real operator otherwise."""
    pytest.importorskip("airflow.providers.standard.operators.bash")
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "ret.json"))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "t")
    runner.run_operator(
        "airflow.providers.standard.operators.bash.BashOperator",
        {"bash_command": "echo generic-op-ran"},
    )
    assert json.loads((tmp_path / "ret.json").read_text()) == "generic-op-ran"


def test_merge_operator_xcom_injects_upstream_values(monkeypatch):
    """An operator arg bound to an upstream (recorded as xcom_input) is delivered
    as LEOFLOW_XCOM_<PARAM>; _merge_operator_xcom must set it on the kwargs,
    overriding any same-name literal (ADR 0040 A1.1). Pure: no Airflow needed."""
    monkeypatch.setenv("LEOFLOW_XCOM_SQL", '"SELECT 42"')
    merged = runner._merge_operator_xcom({"conn_id": "sf", "sql": "PLACEHOLDER"})
    assert merged["sql"] == "SELECT 42"   # XCom overrides the literal
    assert merged["conn_id"] == "sf"      # untouched literal survives


def test_merge_operator_xcom_noop_without_xcom(monkeypatch):
    """With no LEOFLOW_XCOM_* in the env, the operator kwargs pass through."""
    monkeypatch.delenv("LEOFLOW_XCOM_SQL", raising=False)
    merged = runner._merge_operator_xcom({"conn_id": "sf"})
    assert merged == {"conn_id": "sf"}


def test_is_reschedule_exc_matches_by_name():
    """AirflowRescheduleException is recognized by class name across the MRO, so
    the runtime translates reschedule-mode sensors to a clear error without
    importing Airflow (review polish #5)."""
    class AirflowRescheduleException(Exception):
        pass

    class Subclass(AirflowRescheduleException):
        pass

    assert runner._is_reschedule_exc(AirflowRescheduleException())
    assert runner._is_reschedule_exc(Subclass())
    assert not runner._is_reschedule_exc(ValueError("nope"))


def test_run_operator_rejects_reschedule_sensor(monkeypatch):
    """A reschedule-mode sensor is rejected up front with a clear message (review
    polish #5) — its execute() would otherwise crash on a missing TaskInstance.
    Skips without Airflow."""
    pytest.importorskip("airflow.providers.standard.sensors.filesystem")
    monkeypatch.setenv("LEOFLOW_TASK_ID", "s")
    with pytest.raises(RuntimeError, match="reschedule"):
        runner.run_operator(
            "airflow.providers.standard.sensors.filesystem.FileSensor",
            {"filepath": "/nope", "mode": "reschedule", "poke_interval": 1, "timeout": 1},
        )
