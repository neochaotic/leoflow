"""Validate leoflow's Airflow-compatible responses against OpenMetadata's own models.

This exists because the defect it guards lives in OM's TYPE, not in leoflow's
schema. leoflow emitted `"class_ref": {"module_path": null, ...}`, which is valid
JSON, valid against our OpenAPI spec, and rejected by OM's pydantic model, whose
class_ref is `Optional[Dict[str, str]]`. Every DAG failed validation, no pipeline
was ingested, and OM's Test Connection stayed green because it probes
reachability rather than shape. A Go-only test cannot see any of that.

om_models.py is vendored VERBATIM from OpenMetadata 2.0.1-release,
ingestion/src/metadata/ingestion/source/pipeline/airflow/api/models.py. Do not
edit it. Refresh it from the tag when bumping the supported OM version.

Every construction below mirrors the call in OM's own api/client.py that builds
the model, rather than validating our raw bodies wholesale. That distinction is
load-bearing in both directions:

  - OM normalises `tags` from objects to names before constructing, so a naive
    whole-body check would fail on tags that are in fact correct;
  - OM takes a dag run's `execution_date` from `logical_date or execution_date`,
    so a whole-body check would validate a field OM never reads and skip the one
    it does.

The surfaces covered here are the connector's whole HTTP surface, in call order:
POST /auth/token, GET /api/v2/version, the DAG list, one DAG's tasks, its runs,
and one run's task instances.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from om_models import (  # noqa: E402
    AirflowApiDagDetails,
    AirflowApiDagRun,
    AirflowApiTask,
    AirflowApiTaskInstance,
)

API_VERSION = "v2"


def build_dag_details(dag_data: dict, tasks_data: list[dict]) -> AirflowApiDagDetails:
    """The subset of OM's build_dag_details that shapes what the models receive."""
    tags = []
    for tag in dag_data.get("tags") or []:
        name = tag.get("name") if isinstance(tag, dict) else tag if isinstance(tag, str) else None
        if name:
            tags.append(str(name))

    if API_VERSION == "v2":
        schedule = dag_data.get("timetable_summary")
    else:
        schedule = dag_data.get("schedule_interval")
        if isinstance(schedule, dict):
            schedule = schedule.get("value")

    tasks = [
        AirflowApiTask(
            task_id=t["task_id"],
            downstream_task_ids=t.get("downstream_task_ids"),
            owner=t.get("owner"),
            doc_md=t.get("doc_md"),
            start_date=t.get("start_date"),
            end_date=t.get("end_date"),
            class_ref=t.get("class_ref"),
        )
        for t in tasks_data
    ]
    return AirflowApiDagDetails(
        dag_id=dag_data["dag_id"],
        description=dag_data.get("description"),
        fileloc=dag_data.get("fileloc") or dag_data.get("file_loc"),
        is_paused=dag_data.get("is_paused"),
        owners=dag_data.get("owners") or [],
        tags=tags,
        schedule_interval=schedule,
        max_active_runs=dag_data.get("max_active_runs"),
        start_date=dag_data.get("start_date"),
        tasks=tasks,
    )


def build_dag_run(run: dict) -> AirflowApiDagRun:
    """Mirrors OM's get_dag_runs: execution_date comes from logical_date FIRST.

    Passing the raw body to the model instead would be a hole. `logical_date` is
    not a declared field, so with extra="allow" it is stored untyped and a value
    OM would choke on passes silently, while `execution_date`, which leoflow does
    not emit and OM only falls back to, would be the field under test.
    """
    return AirflowApiDagRun(
        dag_run_id=run.get("dag_run_id", ""),
        state=run.get("state"),
        execution_date=run.get("logical_date") or run.get("execution_date"),
        start_date=run.get("start_date"),
        end_date=run.get("end_date"),
    )


def build_task_instance(ti: dict) -> AirflowApiTaskInstance:
    """Mirrors OM's get_task_instances_for_run."""
    return AirflowApiTaskInstance(
        task_id=ti.get("task_id", ""),
        state=ti.get("state"),
        start_date=ti.get("start_date"),
        end_date=ti.get("end_date"),
    )


def check_token(body: object, failures: list[str]) -> None:
    """OM's try_exchange_jwt reads resp.json()["access_token"] and uses it as a
    Bearer for every later call. Anything but a non-empty string there silently
    demotes the connector to Basic auth, which leoflow does not accept, so every
    request 401s."""
    if not isinstance(body, dict):
        failures.append("auth/token: response is not an object")
        return
    token = body.get("access_token")
    if not isinstance(token, str) or not token.strip():
        failures.append(f"auth/token: access_token is {token!r}, want a non-empty string")


def check_version(body: object, failures: list[str]) -> None:
    """OM's test_get_version calls response.json() on this and _detect_api_version
    uses a 2xx here to choose v2 over v1. A body that is not a JSON object fails
    the mandatory CheckAccess step."""
    if not isinstance(body, dict):
        failures.append("version: response is not an object")


def check_envelope(name: str, body: object, key: str, failures: list[str]) -> bool:
    """The invariants OM's _paginate enforces before any model is built: a JSON
    object, a list under the collection key, and an int total_entries if present
    (it drives the loop's termination)."""
    if not isinstance(body, dict):
        failures.append(f"{name}: response is not an object")
        return False
    if not isinstance(body.get(key), list):
        failures.append(f"{name}: {key!r} is not a list")
        return False
    if "total_entries" in body and not isinstance(body["total_entries"], int):
        failures.append(f"{name}: total_entries is not an int")
        return False
    return True


def main(golden_dir: str) -> int:  # noqa: C901
    g = Path(golden_dir)
    read = lambda f: json.loads((g / f).read_text())  # noqa: E731
    token = read("token.json")
    version = read("version.json")
    dags = read("dags.json")
    tasks = read("tasks.json")
    runs = read("dag_runs.json")
    tis = read("task_instances.json")

    failures: list[str] = []
    check_token(token, failures)
    check_version(version, failures)

    envelopes_ok = all(
        [
            check_envelope("dags", dags, "dags", failures),
            check_envelope("tasks", tasks, "tasks", failures),
            check_envelope("dag_runs", runs, "dag_runs", failures),
            check_envelope("task_instances", tis, "task_instances", failures),
        ]
    )
    if not envelopes_ok or failures:
        print("\n".join(failures))
        return 1

    if not dags["dags"]:
        print("fixture is empty: no DAGs to validate, so this gate would be vacuous")
        return 1

    for dag in dags["dags"]:
        try:
            build_dag_details(dag, tasks["tasks"])
        except Exception as exc:  # noqa: BLE001 - the message is the whole point
            failures.append(f"dag {dag.get('dag_id')!r}: {exc}")

    for r in runs["dag_runs"]:
        try:
            build_dag_run(r)
        except Exception as exc:  # noqa: BLE001
            failures.append(f"dag_run {r.get('dag_run_id')!r}: {exc}")

    for ti in tis["task_instances"]:
        try:
            build_task_instance(ti)
        except Exception as exc:  # noqa: BLE001
            failures.append(f"task_instance {ti.get('task_id')!r}: {exc}")

    if failures:
        print("OpenMetadata would reject these; ingestion yields nothing and Test Connection still passes:\n")
        print("\n".join(f"  {f}" for f in failures))
        return 1

    print(f"ok: {len(dags['dags'])} dag(s), {len(tasks['tasks'])} task(s) accepted by OpenMetadata 2.0.1 models")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else "golden"))
