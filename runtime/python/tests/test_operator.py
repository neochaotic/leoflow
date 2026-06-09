"""Airflow-free tests for the generic operator runner (ADR 0040 Phase A).

These exercise ``runner.run_operator`` and the ``--operator`` CLI dispatch with a
fake operator class (no real Airflow), so the operator execution path is covered
in CI where ``apache-airflow`` is intentionally absent.
"""

import itertools
import json

import pytest

from leoflow_runtime import __main__, runner

_counter = itertools.count()

# A fake operator module: each class mimics the surface run_operator touches
# (``__init__(task_id, **kwargs)`` → ``render_template_fields`` → ``execute``),
# so we can drive every branch without installing Airflow.
_FAKE_OPERATOR_MODULE = '''
class EchoOperator:
    mode = None

    def __init__(self, task_id, **kwargs):
        self.task_id = task_id
        self.kwargs = kwargs
        self.rendered = False

    def render_template_fields(self, context):
        self.rendered = True

    def execute(self, context):
        return {"task_id": self.task_id, "kwargs": self.kwargs}


class RescheduleSensor(EchoOperator):
    mode = "reschedule"


class RenderBoomOperator(EchoOperator):
    def render_template_fields(self, context):
        raise ValueError("cannot render")


class NonJsonOperator(EchoOperator):
    def execute(self, context):
        return object()  # not JSON-serializable -> repr fallback


# Named exactly as Airflow's exception: run_operator matches it by class name
# across the MRO (no Airflow import), so this reproduces a reschedule request.
class AirflowRescheduleException(Exception):
    pass


class LateRescheduleSensor(EchoOperator):
    mode = "poke"  # slips past the up-front mode check

    def execute(self, context):
        raise AirflowRescheduleException("reschedule at ...")


# Named exactly as Airflow's exception: a deferrable operator suspends itself by
# raising TaskDeferred from execute(). Leoflow has no triggerer (Phase C), so the
# runtime must translate it into a clear "set deferrable=False" message.
class TaskDeferred(Exception):
    pass


class DeferringOperator(EchoOperator):
    def execute(self, context):
        raise TaskDeferred("deferring to a trigger")
'''


def _write_operator_module(tmp_path, monkeypatch) -> str:
    name = f"fakeops_{next(_counter)}"
    (tmp_path / f"{name}.py").write_text(_FAKE_OPERATOR_MODULE)
    monkeypatch.syspath_prepend(str(tmp_path))
    return name


def test_run_operator_executes_and_writes_return(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "load")
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.EchoOperator", {"bucket": "b1"})

    written = json.loads(out.read_text())
    assert written == {"task_id": "load", "kwargs": {"bucket": "b1"}}


def test_run_operator_requires_dotted_class():
    with pytest.raises(ValueError, match="dotted"):
        runner.run_operator("NotDotted", {})


def test_run_operator_rejects_reschedule_mode(tmp_path, monkeypatch):
    mod = _write_operator_module(tmp_path, monkeypatch)
    with pytest.raises(RuntimeError, match="reschedule-mode sensor"):
        runner.run_operator(f"{mod}.RescheduleSensor", {})


def test_run_operator_render_template_fields_is_best_effort(tmp_path, monkeypatch, capsys):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_operator_module(tmp_path, monkeypatch)

    # A render failure must not fail the task — execute still runs.
    runner.run_operator(f"{mod}.RenderBoomOperator", {})

    assert out.exists()
    assert "render_template_fields skipped" in capsys.readouterr().out


def test_run_operator_translates_late_reschedule_exception(tmp_path, monkeypatch):
    mod = _write_operator_module(tmp_path, monkeypatch)
    with pytest.raises(RuntimeError, match="reschedule"):
        runner.run_operator(f"{mod}.LateRescheduleSensor", {})


def test_run_operator_translates_deferral(tmp_path, monkeypatch):
    # A deferrable operator (deferrable=True) raises TaskDeferred. With no
    # triggerer (Phase C), the runtime must fail with a clear, actionable message
    # naming deferrable — not leak a raw TaskDeferred traceback.
    mod = _write_operator_module(tmp_path, monkeypatch)
    with pytest.raises(RuntimeError, match="deferrable"):
        runner.run_operator(f"{mod}.DeferringOperator", {})


def test_run_operator_merges_upstream_xcom(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_XCOM_SQL", '"SELECT 1"')
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.EchoOperator", {"bucket": "b1"})

    written = json.loads(out.read_text())
    # The upstream XCom is delivered as the ``sql`` kwarg alongside the literal.
    assert written["kwargs"] == {"bucket": "b1", "sql": "SELECT 1"}


def test_run_operator_stringifies_non_json_return(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.NonJsonOperator", {})

    # A non-serializable return is stringified (repr) rather than failing.
    assert json.loads(out.read_text()).startswith("<object object")


def test_operator_context_parses_params(monkeypatch):
    monkeypatch.setenv("LEOFLOW_PARAMS", '{"a": 1}')
    monkeypatch.setenv("LEOFLOW_DS", "2026-06-09")
    ctx = runner._operator_context()
    assert ctx["params"] == {"a": 1}
    assert ctx["ds"] == "2026-06-09"


def test_operator_context_ignores_malformed_params(monkeypatch):
    monkeypatch.setenv("LEOFLOW_PARAMS", "{not json")
    assert runner._operator_context()["params"] == {}


def test_main_operator_dispatch(monkeypatch):
    calls = {}

    def _capture(cls, args):
        calls.update(cls=cls, args=args)

    monkeypatch.setattr(__main__, "run_operator", _capture)
    monkeypatch.setenv("LEOFLOW_OPERATOR_ARGS", '{"bucket": "b1"}')

    assert __main__.main(["--operator", "pkg.mod.MyOperator"]) == 0
    assert calls == {"cls": "pkg.mod.MyOperator", "args": {"bucket": "b1"}}


def test_main_operator_rejects_bad_args_json(monkeypatch, capsys):
    monkeypatch.setattr(__main__, "run_operator", lambda cls, args: pytest.fail("should not run"))
    monkeypatch.setenv("LEOFLOW_OPERATOR_ARGS", "{not json")

    assert __main__.main(["--operator", "pkg.mod.MyOperator"]) == 2
    assert "invalid LEOFLOW_OPERATOR_ARGS JSON" in capsys.readouterr().err
