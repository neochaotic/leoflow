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

The construction below mirrors OM's own build_dag_details (api/client.py) rather
than validating our raw bodies wholesale. That distinction is load-bearing: OM
normalises `tags` from objects to names before constructing the model, so a naive
whole-body validation would fail on tags that are in fact correct, and would hide
the field that actually breaks.
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

    schedule = (
        dag_data.get("timetable_summary")
        if API_VERSION == "v2"
        else (dag_data.get("schedule_interval") or {}).get("value")
        if isinstance(dag_data.get("schedule_interval"), dict)
        else dag_data.get("schedule_interval")
    )

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
        owners=dag_data.get("owners"),
        tags=tags,
        schedule_interval=schedule,
        max_active_runs=dag_data.get("max_active_runs"),
        start_date=dag_data.get("start_date"),
        tasks=tasks,
    )


def main(golden_dir: str) -> int:
    g = Path(golden_dir)
    dags = json.loads((g / "dags.json").read_text())
    tasks = json.loads((g / "tasks.json").read_text())
    runs = json.loads((g / "dag_runs.json").read_text())
    tis = json.loads((g / "task_instances.json").read_text())

    failures: list[str] = []

    # Envelope invariants OM's _paginate enforces before any model is built.
    for name, body, key in (
        ("dags", dags, "dags"),
        ("tasks", tasks, "tasks"),
        ("dag_runs", runs, "dag_runs"),
        ("task_instances", tis, "task_instances"),
    ):
        if not isinstance(body, dict):
            failures.append(f"{name}: response is not an object")
        elif not isinstance(body.get(key), list):
            failures.append(f"{name}: {key!r} is not a list")
        elif "total_entries" in body and not isinstance(body["total_entries"], int):
            failures.append(f"{name}: total_entries is not an int")

    if failures:
        print("\n".join(failures))
        return 1

    for dag in dags["dags"]:
        try:
            build_dag_details(dag, tasks["tasks"])
        except Exception as exc:  # noqa: BLE001 - the message is the whole point
            failures.append(f"dag {dag.get('dag_id')!r}: {exc}")

    for r in runs["dag_runs"]:
        try:
            AirflowApiDagRun(**r)
        except Exception as exc:  # noqa: BLE001
            failures.append(f"dag_run {r.get('dag_run_id')!r}: {exc}")

    for ti in tis["task_instances"]:
        try:
            AirflowApiTaskInstance(**ti)
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
