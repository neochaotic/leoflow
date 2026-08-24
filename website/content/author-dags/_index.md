---
title: Author DAGs
linkTitle: Author DAGs
weight: 20
description: Author DAGs on the Airflow SDK — operators, dbt, variables & connections, alerting, map-reduce, and worked examples.
cascade: { type: docs }
menu:
  main:
    weight: 20
---

Everything about writing DAGs for Leoflow. A DAG is a `dag.py` on the Airflow Task
SDK plus a `leoflow.yaml` for packaging and bindings, compiled to one immutable
artifact.

- **[DAG authoring](/author-dags/dag-authoring/)** — the project layout, the two
  files, and the compile model.
- **[Operators & sensors](/author-dags/operators-sensors/)** — use Airflow
  operators and sensors from your tasks.
- **[dbt projects as DAGs](/author-dags/dbt/)** — render a dbt project into
  model-level tasks.
- **[Variables & Connections](/author-dags/variables-connections/)** — expose
  Variables and Connections to your task pods.
- **[On-failure alerting](/author-dags/alerting/)** — notify on run failure from
  `leoflow.yaml`, no extra task.
- **[Map-reduce for ML](/author-dags/map-reduce/)** — fan-out + reduce as a Python
  list comprehension.
- **[Examples](/author-dags/examples/)** — runnable example DAGs.
- **[ETL case study](/author-dags/etl-case-study/)** — a worked 1 GB ETL on the
  staging volume.
- **[The Lite web editor](/author-dags/lite-web-editor/)** — edit and run DAGs from
  the browser.
