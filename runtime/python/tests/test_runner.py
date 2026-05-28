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


def test_run_without_return_writes_no_file(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_module(tmp_path, monkeypatch, "def task():\n    return None\n")

    runner.run(f"{mod}:task")

    assert not out.exists()


def test_run_rejects_bad_entrypoint():
    with pytest.raises(ValueError):
        runner.run("no_callable_here")


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
