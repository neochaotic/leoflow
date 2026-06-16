---
connectors: [pagerduty]
---

# PagerDuty connection

Trigger PagerDuty incidents and alerts from a task over a managed Leoflow
Connection. `PagerdutyHook` uses a REST API token for the REST API and an
Events-API routing key (integration key) for Events v2 alerts.

## URI shape

```
pagerduty://:<api_token>@?__extra__=<json>
```

There is no host. The REST API token lives in Password (percent-escaped),
and the Events-API routing key rides in `Extra` under `__extra__`.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `pagerduty_default`. Exported as `AIRFLOW_CONN_PAGERDUTY_DEFAULT`. |
| Conn Type | yes | `pagerduty`. |
| Password | yes | The REST API token. Encrypted at rest (ADR 0019). |
| Extra | yes | JSON: `{"routing_key":"R0UTINGKEY"}` — the Events v2 integration key. |
| Host | no | Leave blank. |

## Example DAG

```python
from airflow.sdk import dag, task


@dag(schedule=None, catchup=False, tags=["pagerduty"])
def pagerduty_alert():
    @task
    def trigger():
        from airflow.providers.pagerduty.hooks.pagerduty_events import (
            PagerdutyEventsHook,
        )

        hook = PagerdutyEventsHook(pagerduty_events_conn_id="pagerduty_default")
        hook.create_event(
            summary="DAG failed",
            severity="critical",
            source="leoflow",
        )

    trigger()


pagerduty_alert()
```

```yaml
# leoflow.yaml
dag_id: pagerduty_alert
runtime: python3.12
requirements:
  - apache-airflow-providers-pagerduty
connections:
  - conn_id: pagerduty_default
    conn_type: pagerduty
```

## Security notes

- **Two distinct secrets**: the REST API token (account-scoped, in
  Password) and the routing key (service-scoped, in `Extra`). Use the
  routing key for alerts so a leak is blast-radius-limited to one service.
- **Rotate tokens** in PagerDuty user/account settings; the routing key
  rotates by recreating the integration.
- Never log `AIRFLOW_CONN_PAGERDUTY_DEFAULT`.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestPagerdutyConnectionURIShapeIntegration` — chain-of-custody delivery test.
