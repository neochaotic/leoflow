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


def test_generic_provider_operator_is_captured(monkeypatch, tmp_path):
    """A top-level provider OPERATOR (the task itself) is captured generically
    instead of rejected: the shim synthesizes the operator class, the parser emits
    type=airflow_operator with the real class path + the constructor kwargs, which
    the runtime later instantiates and executes (ADR 0040 Phase A)."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator
        with DAG("g"):
            SQLExecuteQueryOperator(task_id="q", conn_id="sf", sql="SELECT 1")
    """)
    t = next(x for x in spec["tasks"] if x["task_id"] == "q")
    assert t["type"] == "airflow_operator"
    assert t["operator_class"] == \
        "airflow.providers.snowflake.operators.snowflake.SQLExecuteQueryOperator"
    assert t["operator_args"]["conn_id"] == "sf"
    assert t["operator_args"]["sql"] == "SELECT 1"


def test_generic_provider_sensor_is_captured(monkeypatch, tmp_path):
    """A provider SENSOR is captured too, carrying its poke mode (ADR 0040)."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.amazon.aws.sensors.s3 import S3KeySensor
        with DAG("g"):
            S3KeySensor(task_id="wait", bucket_key="k", bucket_name="b", mode="poke")
    """)
    t = next(x for x in spec["tasks"] if x["task_id"] == "wait")
    assert t["type"] == "airflow_operator"
    assert t["operator_class"] == "airflow.providers.amazon.aws.sensors.s3.S3KeySensor"
    assert t["operator_args"]["bucket_key"] == "k"


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


def test_operator_arg_bound_to_upstream_becomes_xcom_input(monkeypatch, tmp_path):
    """A generic-operator arg set to an upstream task's output is wired as an
    xcom_input (ADR 0040 A1.1), not silently dropped; sibling literals still
    land in operator_args."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator
        @task
        def make_sql() -> str: ...
        with DAG("g"):
            s = make_sql()
            SQLExecuteQueryOperator(task_id="q", conn_id="sf", sql=s)
    """)
    q = _task(spec, "q")
    assert q["xcom_input"] == {"sql": ["make_sql"]}
    assert "sql" not in q.get("operator_args", {})
    assert q["operator_args"]["conn_id"] == "sf"


def test_operator_non_serialisable_arg_is_loud_reject(monkeypatch, tmp_path):
    """A non-JSON, non-XCom operator arg (e.g. a callable) is a loud compile
    error, not a silent drop (ADR 0040 A1.1)."""
    import pytest
    with pytest.raises(ValueError):
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG
            from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator
            with DAG("g"):
                SQLExecuteQueryOperator(task_id="q", conn_id="sf", sql="SELECT 1",
                                        on_success_callback=lambda ctx: None)
        """)


def test_http_sensor_is_generic_operator_not_native_http(monkeypatch, tmp_path):
    """A SENSOR whose name contains 'Http' (HttpSensor) must be captured as a
    generic airflow_operator (poke in a pod), NOT mistranslated to the native
    http_api inline call. Regression for the substring fast-path (e2e finding)."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.http.sensors.http import HttpSensor
        with DAG("g"):
            HttpSensor(task_id="probe", http_conn_id="cp", endpoint="readyz")
    """)
    probe = _task(spec, "probe")
    assert probe["type"] == "airflow_operator"
    assert probe["operator_class"] == "airflow.providers.http.sensors.http.HttpSensor"


def test_http_operator_stays_native_http_api(monkeypatch, tmp_path):
    """HttpOperator (an operator, not a sensor) keeps its native http_api fast path."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.http.operators.http import HttpOperator
        with DAG("g"):
            HttpOperator(task_id="call", method="GET", endpoint="https://example.com/x")
    """)
    assert _task(spec, "call")["type"] == "http_api"


@pytest.mark.parametrize("class_name", [
    "AcmePythonModelOperator",  # contains "Python"
    "AcmeBashStyleOperator",    # contains "Bash"
    "AcmeHttpCallOperator",     # contains "Http"
])
def test_generic_operator_with_native_substring_name_is_captured(
        monkeypatch, tmp_path, class_name):
    """A long-tail provider OPERATOR (captured by _generic) whose class name happens
    to contain Bash/Http/Python must route to airflow_operator — NOT the native task
    type the substring fast-path infers. The fast-path exists only for the bundled
    shim operators (which carry no __leoflow_operator_class__); routing a captured
    class natively would drop its operator_class and silently mistranslate it (same
    class of bug as the HttpSensor regression above)."""
    spec = _compile(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from airflow.providers.acme.operators.run import {class_name}
        with DAG("g"):
            {class_name}(task_id="t")
    """)
    t = _task(spec, "t")
    assert t["type"] == "airflow_operator"
    assert t["operator_class"] == f"airflow.providers.acme.operators.run.{class_name}"
