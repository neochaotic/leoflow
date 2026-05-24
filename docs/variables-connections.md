# Variables & Connections

Leoflow stores **Variables** and **Connections** in the control plane (connection
secrets encrypted at rest, AES-256-GCM — ADR 0019) and delivers them to task pods
at runtime as environment variables, so your task reads them with the **native
Airflow APIs** *and* as plain env (ADR 0021).

## Manage them
Via the Airflow-compatible UI (Admin → Variables / Connections) or the API:

```bash
# Variable
curl -X POST "$LEOFLOW_SERVER/api/v2/variables" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"key":"greeting","value":"hello"}'

# Connection (password + extra are encrypted at rest)
curl -X POST "$LEOFLOW_SERVER/api/v2/connections" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"connection_id":"warehouse","conn_type":"postgres","host":"db","login":"u","password":"p","schema":"analytics"}'
```

## Read them in a task
The agent injects each tenant's Variables/Connections before running your code:

- `AIRFLOW_VAR_<KEY>` (uppercased) → `Variable.get("key")`
- `AIRFLOW_CONN_<ID>` (a connection URI, with `extra` carried under `__extra__`) → `BaseHook.get_connection("id")`

```python
from airflow.sdk import task

@task
def use_secrets():
    import os
    from airflow.sdk import Variable          # native Airflow API
    print(Variable.get("greeting"))           # "hello"
    print(os.environ["AIRFLOW_VAR_GREETING"]) # also a plain env var
```

Scope is global (per tenant). Delivery requires a secure agent channel (TLS, #58)
or, in dev, the explicit `LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true` (set by
`leoflow dev`). See [ADR 0021](adr/0021-exposing-variables-connections-to-pods.md).
