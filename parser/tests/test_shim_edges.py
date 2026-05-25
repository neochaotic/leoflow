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


def test_sibling_module_import_resolves(tmp_path):
    """A DAG importing a helper module next to it must compile (Airflow's DagBag
    puts the DAG folder on sys.path; the shim loader must too)."""
    (tmp_path / "helpers.py").write_text("def names():\n    return ['x', 'y', 'z']\n")
    spec = _compile(tmp_path, """
        from airflow.sdk import DAG, task
        from helpers import names
        @task
        def step() -> None: ...
        with DAG("g"):
            for _ in names():
                step()
    """)
    assert len(spec["tasks"]) == 3  # one per helper-provided id (deduped)


def test_sibling_modules_are_isolated_between_compiles(tmp_path, tmp_path_factory):
    """Two DAGs in different dirs with a same-named helper must not bleed state."""
    d1 = tmp_path_factory.mktemp("one")
    d2 = tmp_path_factory.mktemp("two")
    (d1 / "shared.py").write_text("N = 1\n")
    (d2 / "shared.py").write_text("N = 4\n")
    for d in (d1, d2):
        (d / "leoflow.yaml").write_text("schema_version: '1.0'\n")
        (d / "dag.py").write_text(
            "from airflow.sdk import DAG, task\n"
            "from shared import N\n"
            "@task\ndef t() -> None: ...\n"
            "with DAG('g'):\n"
            "    [t() for _ in range(N)]\n"
        )
    from leoflow_parser.compiler import compile_dag
    s1 = compile_dag(str(d1 / "dag.py"), str(d1 / "leoflow.yaml"), "x:v")
    s2 = compile_dag(str(d2 / "dag.py"), str(d2 / "leoflow.yaml"), "x:v")
    assert len(s1["tasks"]) == 1 and len(s2["tasks"]) == 4


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
