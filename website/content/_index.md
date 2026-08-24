---
title: Leoflow
linkTitle: Home
menu:
  main:
    weight: 10
---

{{< blocks/lead color="primary" >}}
# Leoflow

The workflow orchestrator that ate Apache Airflow's lunch.

Compatible with the Apache Airflow UI &amp; REST API — a **Go control plane** instead
of Python's. Zero of the pain. Native map-reduce for ML/AI: fan-out + reduce as a
Python list comprehension.
{{< /blocks/lead >}}

{{% blocks/section color="dark" type="row" %}}
{{% blocks/feature icon="fa-solid fa-bolt" title="A Go control plane" %}}
Keeps Airflow's proven pod-per-task model and its UI, and throws away the Python
control plane that makes Airflow slow.
{{% /blocks/feature %}}

{{% blocks/feature icon="fa-solid fa-cube" title="DAGs are immutable artifacts" %}}
A `leoflow.yaml` plus a `dag.py` compile to one immutable artifact — `dag.json` +
a container image. Parsed once, at compile time.
{{% /blocks/feature %}}

{{% blocks/feature icon="fa-brands fa-github" title="GitOps-first" %}}
Every DAG is a versioned, immutable artifact built in CI. A real dev loop with
`leoflow lite` — isolated cluster, hot reload.
{{% /blocks/feature %}}
{{% /blocks/section %}}
