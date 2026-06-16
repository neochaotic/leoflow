# Amazon EMR connection

Submit and monitor Amazon EMR steps / job runs from a managed Leoflow
Connection. EMR carries no host and no password — only the AWS region lives in
**Extra**, and auth is keyless IAM.

## Declare the provider

```yaml
connectors: [emr]
```

## URI shape

EMR is an **Extra-only** connection (no host, no credentials in the URI):

```
emr://?__extra__=<url-encoded JSON>
```

The control plane delivers it as `AIRFLOW_CONN_<ID>`; the `__extra__` query
parameter carries `{"region_name":"..."}`. The chain-of-custody test
`TestEmrConnectionURIShapeIntegration` pins the scheme and the `__extra__`
round-trip.

## Fields

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `emr_batch`. Exported as `AIRFLOW_CONN_EMR_BATCH`. |
| Conn Type | yes | `emr`. |
| Extra | yes | JSON: `{"region_name":"eu-central-1"}`. |

No Login/Password: EMR auth is **keyless IAM only** in this connector
(see Security).

## Example DAG (doc-only)

The provider import goes **inside** the `@task` body — a top-level provider
import fails the compile.

```python
from datetime import datetime
from airflow.decorators import dag, task


@dag(schedule=None, start_date=datetime(2024, 1, 1), catchup=False)
def emr_demo():
    @task
    def list_clusters() -> int:
        from airflow.providers.amazon.aws.hooks.emr import EmrHook

        hook = EmrHook(aws_conn_id="emr_batch")
        client = hook.get_conn()
        resp = client.list_clusters(ClusterStates=["WAITING", "RUNNING"])
        return len(resp.get("Clusters", []))

    list_clusters()


emr_demo()
```

```yaml
# leoflow.yaml
dag_id: emr_demo
python: "3.12"
connectors: [emr]
```

## Security notes

- **Keyless-first (the only supported posture here).** Auth flows through the
  task's IAM role (instance profile / IRSA on EKS); the Connection holds no
  secret ([ADR 0035](../adr/0035-cloud-connector-auth-keyless-first.md)). Grant
  the role the minimal `elasticmapreduce:*` actions your DAG needs.
- Secrets (none for EMR) and config travel only over the authenticated, TLS
  agent channel (ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- ADR 0035 — keyless-first cloud connector auth.
- [athena.md](athena.md), [redshift.md](redshift.md) — the other AWS data services in this batch.
