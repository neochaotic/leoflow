"""Tests for the ``python -m leoflow_runtime`` CLI entry point."""

import json

from leoflow_runtime import __main__


def test_main_requires_exactly_one_arg():
    assert __main__.main([]) == 2
    assert __main__.main(["a", "b"]) == 2


def test_main_runs_entrypoint(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    (tmp_path / "climod.py").write_text("def task():\n    return 'ok'\n")
    monkeypatch.syspath_prepend(str(tmp_path))

    assert __main__.main(["climod:task"]) == 0
    assert json.loads(out.read_text()) == "ok"


def test_main_bash_dispatch(monkeypatch):
    # --bash routes to run_bash (which renders + execs bash in place; mocked here).
    calls = {}
    monkeypatch.setattr(__main__, "run_bash", lambda cmd: calls.update(cmd=cmd))

    assert __main__.main(["--bash", "echo {{ ds }}"]) == 0
    assert calls == {"cmd": "echo {{ ds }}"}
