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


def test_run_emits_pre_and_post_lifecycle_groups(tmp_path, monkeypatch, capsys):
    """Stdout MUST carry ::group::Pre task execution ... ::endgroup:: around
    the runtime's setup lines (loading, kwargs), and a matching Post group
    around the return summary. The Airflow 3.2 SPA log viewer renders these
    markers as collapsible sections; without them the log panel mixes
    framework noise with user output. User print() lands BETWEEN the two
    groups so it stays visible by default.
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
    # The two group pairs MUST appear, in order, and the user print MUST land
    # BETWEEN them (not inside either).
    pre_open = stdout.find("::group::Pre task execution")
    pre_close = stdout.find("::endgroup::", pre_open)
    user_line = stdout.find("USER_LINE_INSIDE_TASK")
    post_open = stdout.find("::group::Post task execution", pre_close)
    post_close = stdout.find("::endgroup::", post_open)

    assert pre_open != -1, f"missing Pre group open in:\n{stdout}"
    assert pre_close != -1, f"missing Pre group close in:\n{stdout}"
    assert user_line != -1, f"missing user print in:\n{stdout}"
    assert post_open != -1, f"missing Post group open in:\n{stdout}"
    assert post_close != -1, f"missing Post group close in:\n{stdout}"
    assert pre_open < pre_close < user_line < post_open < post_close, (
        f"ordering wrong: Pre {pre_open}-{pre_close}, user {user_line}, "
        f"Post {post_open}-{post_close} in:\n{stdout}"
    )
    # The runtime's lifecycle prefix MUST be present in both groups.
    assert "[leoflow] loading" in stdout
    assert "[leoflow] returned" in stdout


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
