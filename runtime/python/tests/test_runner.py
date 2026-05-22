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
