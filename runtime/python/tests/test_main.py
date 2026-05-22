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
