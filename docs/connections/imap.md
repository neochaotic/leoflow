# IMAP connection

Connect a task to an IMAP mailbox over a managed Leoflow Connection — to poll
for incoming files or messages. The host, port, and credentials are encrypted
at rest and delivered to the task as `AIRFLOW_CONN_<CONN_ID>`.

```yaml
connectors: [imap]
```

## URI shape

```
imap://<login>:<password>@<host>:<port>
```

The control plane percent-escapes the password; `ImapHook` (which parses
`AIRFLOW_CONN_<ID>`) un-escapes it back.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `imap_default`. Exported as `AIRFLOW_CONN_IMAP_DEFAULT`. |
| Conn Type | yes | `imap`. |
| Host | yes | The IMAP host, e.g. `imap.example.com`. |
| Port | optional | Defaults to `993` (implicit TLS). |
| Login | yes | The mailbox username. |
| Password | yes | Stored encrypted at rest (ADR 0019). Percent-escaped in the URI. |
| Extra | optional | JSON for hook-specific options. |

## Example DAG

The hook is imported inside the task body so DAG parsing stays import-light.

```python
from airflow.decorators import dag, task
from pendulum import datetime


@dag(schedule=None, start_date=datetime(2026, 1, 1), catchup=False)
def imap_poll():
    @task
    def check():
        from airflow.providers.imap.hooks.imap import ImapHook

        with ImapHook(imap_conn_id="imap_default") as hook:
            return hook.has_mail_attachment("report-*.csv")

    check()


imap_poll()
```

```yaml
# leoflow.yaml
connectors: [imap]
```

## Security notes

- **TLS in transit**: prefer port `993` (implicit TLS) over plain `143`.
- **Secrets in logs**: never `print()` the URI — it carries the password.
  Log the host + login only.
- **gRPC channel (agent ↔ control plane)**: secrets are served only over an
  authenticated channel (ADR 0021); Pro must run with TLS.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #67 — connectors umbrella.
- #138 — the chain-of-custody contract test this page documents.
