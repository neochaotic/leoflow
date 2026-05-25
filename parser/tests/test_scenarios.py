"""Broad scenario coverage for the shim-backed compiler (ADR 0024).

Supported scenarios assert the resulting structure; unsupported scenarios assert
a clear "not supported by Leoflow" error. Parity of the supported cases with real
Airflow was verified against `LEOFLOW_PARSER_BACKEND=airflow` (schedule forms,
trigger rules, classic PythonOperator, dag= kwarg, dedup, fan-in).
"""
from __future__ import annotations

import textwrap
from pathlib import Path

import pytest
import yaml

from leoflow_parser.compiler import compile_dag


def _compile(tmp_path: Path, body: str, config: dict | None = None) -> dict:
    (tmp_path / "leoflow.yaml").write_text(yaml.safe_dump(config or {"schema_version": "1.0"}))
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(body))
    return compile_dag(str(src), str(tmp_path / "leoflow.yaml"), "img:v1", dag_version="v1")


def _task(spec: dict, task_id: str) -> dict:
    return next(t for t in spec["tasks"] if t["task_id"] == task_id)


# ─────────────────────────── supported scenarios ───────────────────────────

@pytest.mark.parametrize("expr,want", [
    ("None", None),
    ('"0 6 * * *"', "0 6 * * *"),
    ('"@daily"', "@daily"),
    ('"@hourly"', "@hourly"),
])
def test_schedule_forms(tmp_path, expr, want):
    spec = _compile(tmp_path, f"""
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", schedule={expr}):
            a()
    """)
    assert spec.get("schedule") == want


def test_classic_python_operator(tmp_path):
    spec = _compile(tmp_path, """
        from airflow.providers.standard.operators.python import PythonOperator
        from airflow.sdk import DAG
        def work(): ...
        with DAG("g"):
            PythonOperator(task_id="run", python_callable=work)
    """)
    t = _task(spec, "run")
    assert t["type"] == "python" and t["entrypoint"] == "dag:work"


def test_operator_trigger_rule_emitted(tmp_path):
    spec = _compile(tmp_path, """
        from airflow.providers.standard.operators.bash import BashOperator
        from airflow.sdk import DAG
        with DAG("g"):
            a = BashOperator(task_id="a", bash_command="echo a")
            b = BashOperator(task_id="b", bash_command="echo b", trigger_rule="all_done")
            a >> b
    """)
    assert _task(spec, "b")["trigger_rule"] == "all_done"


def test_duplicate_task_id_is_suffixed(tmp_path):
    spec = _compile(tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def w() -> None: ...
        with DAG("g"):
            [w() for _ in range(3)]
    """)
    assert {t["task_id"] for t in spec["tasks"]} == {"w", "w__1", "w__2"}


def test_fan_in_list_adds_all_upstream(tmp_path):
    spec = _compile(tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def part() -> int: return 1
        @task
        def combine(xs: list) -> None: ...
        with DAG("g"):
            combine([part() for _ in range(3)])
    """)
    assert _task(spec, "combine")["depends_on"] == ["part", "part__1", "part__2"]


def test_dag_id_selection_with_multiple_dags(tmp_path):
    spec = _compile(tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("first"):
            a()
        with DAG("second"):
            a()
    """, config={"schema_version": "1.0", "dag_id": "second"})
    assert spec["dag_id"] == "second"


# ────────────────────────── unsupported scenarios ──────────────────────────

@pytest.mark.parametrize("body", [
    # dynamic task mapping
    """
    from airflow.sdk import DAG, task
    @task
    def a(x): ...
    with DAG("g"):
        a.expand(x=[1, 2, 3])
    """,
    # task groups
    """
    from airflow.sdk import DAG
    from airflow.utils.task_group import TaskGroup
    with DAG("g"):
        with TaskGroup("grp"):
            pass
    """,
    # chain helper
    """
    from airflow.sdk import DAG, task, chain
    @task
    def a(): ...
    with DAG("g"):
        a()
    """,
    # an unsupported provider operator
    """
    from airflow.sdk import DAG
    from airflow.providers.amazon.aws.operators.s3 import S3CreateBucketOperator
    with DAG("g"):
        S3CreateBucketOperator(task_id="b", bucket_name="z")
    """,
])
def test_unsupported_constructs_error_clearly(tmp_path, body):
    with pytest.raises(ValueError) as ei:
        _compile(tmp_path, body)
    assert "not supported by Leoflow" in str(ei.value)


def test_unsupported_trigger_rule_errors(tmp_path):
    with pytest.raises(ValueError) as ei:
        _compile(tmp_path, """
            from airflow.providers.standard.operators.bash import BashOperator
            from airflow.sdk import DAG
            with DAG("g"):
                BashOperator(task_id="a", bash_command="x", trigger_rule="none_failed")
        """)
    assert "trigger rule" in str(ei.value)
