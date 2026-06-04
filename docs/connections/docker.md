---
connectors: [docker]
---

# Docker registry connection

Connect a task to a Docker registry (Docker Hub, GHCR, ECR, a private
Harbor/Nexus) over a managed Leoflow Connection. `DockerHook` authenticates
to the registry so the `DockerOperator` can pull/run images.

## URI shape

```
docker://<login>:<password>@<host>:<port>?__extra__=<json>
```

The control plane builds this from the Connection's fields and exports it
as `AIRFLOW_CONN_<CONN_ID>`. The password is percent-escaped; registry
options ride in `Extra` under `__extra__`.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `docker_registry`. Exported as `AIRFLOW_CONN_DOCKER_REGISTRY`. |
| Conn Type | yes | `docker`. |
| Host | yes | Registry host, e.g. `registry.example.com`. |
| Port | optional | Registry port, e.g. `5000`. |
| Login | yes | Registry username. |
| Password | yes | Registry password / access token. Encrypted at rest (ADR 0019). |
| Extra | optional | JSON: `{"email":"a@b.com","reauth":false}`. |

## Example DAG

```python
from airflow.sdk import dag, task


@dag(schedule=None, catchup=False, tags=["docker"])
def docker_login():
    @task
    def whoami():
        from airflow.providers.docker.hooks.docker import DockerHook

        hook = DockerHook(docker_conn_id="docker_registry")
        client = hook.get_conn()
        print("api version:", client.version()["ApiVersion"])

    whoami()


docker_login()
```

```yaml
# leoflow.yaml
dag_id: docker_login
runtime: python3.12
requirements:
  - apache-airflow-providers-docker
connections:
  - conn_id: docker_registry
    conn_type: docker
```

## Security notes

- **Use access tokens, not passwords**: Docker Hub and GHCR support
  scoped PATs; prefer them over account passwords.
- **TLS**: registries should be HTTPS. Plain-HTTP registries require an
  insecure-registry daemon flag — avoid in production.
- Never log `AIRFLOW_CONN_DOCKER_REGISTRY`; it carries the password.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestDockerConnectionURIShapeIntegration` — chain-of-custody delivery test.
