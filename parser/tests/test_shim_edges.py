"""Edge cases the 13 example goldens don't cover — found during the Phase 1/2
review (ADR 0024). Each guards a fidelity gap in the structural shim.
"""
from __future__ import annotations

import textwrap
from pathlib import Path

import pytest
import yaml

from leoflow_parser.compiler import compile_dag


def _compile(tmp_path: Path, body: str) -> dict:
    (tmp_path / "leoflow.yaml").write_text(yaml.safe_dump({"schema_version": "1.0"}))
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(body))
    return compile_dag(str(src), str(tmp_path / "leoflow.yaml"), "img:v1")


def _task(spec: dict, task_id: str) -> dict:
    return next(t for t in spec["tasks"] if t["task_id"] == task_id)


def test_task_decorator_trigger_rule_is_preserved(tmp_path):
    """@task(trigger_rule=…) must reach the dag.json (was silently dropped)."""
    spec = _compile(tmp_path, """
        from airflow.sdk import DAG, task
        @task(trigger_rule="all_done")
        def a() -> None: ...
        @task
        def b() -> None: ...
        with DAG("g"):
            a() >> b()
    """)
    assert _task(spec, "a").get("trigger_rule") == "all_done"


def test_operator_attached_via_dag_kwarg_without_context(tmp_path):
    """BashOperator(dag=dag) outside a `with` block is still collected."""
    spec = _compile(tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        dag = DAG("g")
        BashOperator(task_id="t", bash_command="echo hi", dag=dag)
    """)
    assert spec["dag_id"] == "g"
    assert _task(spec, "t")["type"] == "bash"


def test_missing_sdk_helper_gives_clear_unsupported_error(tmp_path):
    """`from airflow.sdk import chain` (a name the shim lacks) is a clear error,
    not a raw ImportError."""
    with pytest.raises(ValueError) as ei:
        _compile(tmp_path, """
            from airflow.sdk import DAG, task, chain
            @task
            def a() -> None: ...
            with DAG("g"):
                a()
        """)
    assert "not supported by Leoflow" in str(ei.value)
