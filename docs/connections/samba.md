# Samba connection

Connect a task to an SMB/CIFS file share over a managed Leoflow Connection.
The host, port, credentials, and an Extra blob (`share_type`) are encrypted at
rest and delivered to the task as `AIRFLOW_CONN_<CONN_ID>`.

```yaml
connectors: [samba]
```

## URI shape

```
samba://<login>:<password>@<host>:<port>?__extra__=<json>
```

The control plane percent-escapes the password; `SambaHook` (which parses
`AIRFLOW_CONN_<ID>`) un-escapes it back. The Extra blob rides in `__extra__`.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `samba_default`. Exported as `AIRFLOW_CONN_SAMBA_DEFAULT`. |
| Conn Type | yes | `samba`. |
| Host | yes | The file server host, e.g. `files.example.com`. |
| Port | optional | Defaults to `445` (SMB over TCP). |
| Login | yes | The SMB username (optionally `DOMAIN\\user`). |
| Password | yes | Stored encrypted at rest (ADR 0019). Percent-escaped in the URI. |
| Extra | optional | JSON, e.g. `{"share_type":"smb2"}`. |

## Example DAG

The hook is imported inside the task body so DAG parsing stays import-light.

```python
from airflow.decorators import dag, task
from pendulum import datetime


@dag(schedule=None, start_date=datetime(2026, 1, 1), catchup=False)
def samba_pull():
    @task
    def download():
        from airflow.providers.samba.hooks.samba import SambaHook

        hook = SambaHook(samba_conn_id="samba_default")
        hook.get_file("/incoming/report.csv", "/tmp/report.csv")

    download()


samba_pull()
```

```yaml
# leoflow.yaml
connectors: [samba]
```

## Security notes

- **Prefer SMB2/SMB3**: set `{"share_type":"smb2"}` in Extra; avoid the
  legacy SMB1 dialect.
- **Secrets in logs**: never `print()` the URI — it carries the password.
  Log the host + login only.
- **gRPC channel (agent ↔ control plane)**: secrets are served only over an
  authenticated channel (ADR 0021); Pro must run with TLS.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #67 — connectors umbrella.
- #138 — the chain-of-custody contract test this page documents.
