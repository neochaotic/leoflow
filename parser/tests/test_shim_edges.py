"""Edge cases the 13 example goldens don't cover — found during the Phase 1/2
review (ADR 0024). Each guards a fidelity gap in the structural shim.
"""
from __future__ import annotations

import json
import textwrap
from pathlib import Path

import pytest

from leoflow_parser.compiler import compile_dag


def _compile(monkeypatch, tmp_path: Path, body: str) -> dict:
    monkeypatch.setenv(
        "LEOFLOW_PROJECT_CONFIG_JSON",
        json.dumps({"schema_version": "1.0"}),
    )
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(body))
    return compile_dag(str(src), str(tmp_path / "ignored.json"), "img:v1")


def _task(spec: dict, task_id: str) -> dict:
    return next(t for t in spec["tasks"] if t["task_id"] == task_id)


def test_task_decorator_trigger_rule_is_preserved(monkeypatch, tmp_path):
    """@task(trigger_rule=…) must reach the dag.json (was silently dropped)."""
    spec = _compile(monkeypatch, tmp_path,"""
        from airflow.sdk import DAG, task
        @task(trigger_rule="all_done")
        def a() -> None: ...
        @task
        def b() -> None: ...
        with DAG("g"):
            a() >> b()
    """)
    assert _task(spec, "a").get("trigger_rule") == "all_done"


def test_operator_attached_via_dag_kwarg_without_context(monkeypatch, tmp_path):
    """BashOperator(dag=dag) outside a `with` block is still collected."""
    spec = _compile(monkeypatch, tmp_path,"""
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        dag = DAG("g")
        BashOperator(task_id="t", bash_command="echo hi", dag=dag)
    """)
    assert spec["dag_id"] == "g"
    assert _task(spec, "t")["type"] == "bash"


def test_sibling_module_import_resolves(monkeypatch, tmp_path):
    """A DAG importing a helper module next to it must compile (Airflow's DagBag
    puts the DAG folder on sys.path; the shim loader must too)."""
    (tmp_path / "helpers.py").write_text("def names():\n    return ['x', 'y', 'z']\n")
    spec = _compile(monkeypatch, tmp_path,"""
        from airflow.sdk import DAG, task
        from helpers import names
        @task
        def step() -> None: ...
        with DAG("g"):
            for _ in names():
                step()
    """)
    assert len(spec["tasks"]) == 3  # one per helper-provided id (deduped)


def test_sibling_modules_are_isolated_between_compiles(monkeypatch, tmp_path_factory):
    """Two DAGs in different dirs with a same-named helper must not bleed state."""
    d1 = tmp_path_factory.mktemp("one")
    d2 = tmp_path_factory.mktemp("two")
    (d1 / "shared.py").write_text("N = 1\n")
    (d2 / "shared.py").write_text("N = 4\n")
    for d in (d1, d2):
        (d / "dag.py").write_text(
            "from airflow.sdk import DAG, task\n"
            "from shared import N\n"
            "@task\ndef t() -> None: ...\n"
            "with DAG('g'):\n"
            "    [t() for _ in range(N)]\n"
        )
    from leoflow_parser.compiler import compile_dag
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps({"schema_version": "1.0"}))
    s1 = compile_dag(str(d1 / "dag.py"), str(d1 / "ignored.json"), "x:v")
    s2 = compile_dag(str(d2 / "dag.py"), str(d2 / "ignored.json"), "x:v")
    assert len(s1["tasks"]) == 1 and len(s2["tasks"]) == 4


def test_missing_sdk_helper_gives_clear_unsupported_error(monkeypatch, tmp_path):
    """`from airflow.sdk import chain` (a name the shim lacks) is a clear error,
    not a raw ImportError."""
    with pytest.raises(ValueError) as ei:
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG, task, chain
            @task
            def a() -> None: ...
            with DAG("g"):
                a()
        """)
    assert "not supported by Leoflow" in str(ei.value)


def test_top_level_provider_import_gives_actionable_message(monkeypatch, tmp_path):
    """A provider hook imported at DAG module top-level fails the parse (the
    parser has no providers installed). The message must NOT claim the provider
    is categorically unsupported (it IS supported at runtime via connectors:);
    it must name the provider, tell the user to import inside the @task body, and
    point at connectors:/dependencies:. ADR 0038 #2."""
    with pytest.raises(ValueError) as ei:
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG, task
            from airflow.providers.postgres.hooks.postgres import PostgresHook
            @task
            def a() -> None: ...
            with DAG("g"):
                a()
        """)
    msg = str(ei.value)
    assert "airflow.providers.postgres" in msg
    assert "connectors:" in msg
    assert "@task" in msg
    # It must NOT fall through to the generic operator-unsupported wording.
    assert "supported: Bash, Http" not in msg
