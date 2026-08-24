---
title: Build your first DAG
linkTitle: Build your first DAG
weight: 20
description: A guided walk from an empty project to a running DAG — scaffold, author, compile, run.
---

{{% alert title="TODO — content gap" color="warning" %}}
This is a **placeholder** for a guided, beginner-oriented walkthrough that takes you
from an empty project directory to a DAG running in the UI. It is a known gap in the
IA and will be written in a follow-up phase.
{{% /alert %}}

Until this guide is written, the fastest path is:

1. **[Quickstart](/get-started/quickstart/)** — get Leoflow Lite running locally in
   two commands.
2. **[DAG authoring](/author-dags/dag-authoring/)** — the project layout, the two
   files (`dag.py` + `leoflow.yaml`), and the compile model.
3. **[Examples](/author-dags/examples/)** — runnable DAGs you can copy and adapt.

Planned outline for this page:

- Scaffold a project with `leoflow init dags/my_first`.
- Write a three-task ETL on the Airflow Task SDK.
- Declare packaging and dependencies in `leoflow.yaml`.
- Run it with `leoflow lite` and watch it in the UI.
- Break it on purpose and read the import-error banner.
