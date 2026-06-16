# MongoDB connection

Connect a task to MongoDB via a managed Leoflow Connection and `MongoHook`. The
conn_type is `mongo`. It is the host:port + login/password shape; the **Schema**
field carries the database (or auth database).

## Declare the provider

```yaml
# leoflow.yaml
dag_id: mongo_load
connectors:
  - mongo
```

## Fields the UI asks for

| Field | Notes |
|---|---|
| Host | Mongo host (or the first of a replica set). |
| Port | Defaults to `27017`. |
| Login / Password | The Mongo user. Password encrypted at rest (ADR 0019). |
| Schema | The database, e.g. `analytics`. |
| Extra | `{"srv": true}` for `mongodb+srv://` (Atlas), `{"authSource": "admin"}`, TLS options. |

The host:port + login/password + db round-trip is covered by the table-driven
`TestConnectionDeliveryChainOfCustodyIntegration` (mongo row).

## Example DAG (copy-paste)

Docs-only recipe (needs a MongoDB instance). Hook imported **inside** the task.

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def load() -> None:
    from airflow.providers.mongo.hooks.mongo import MongoHook

    hook = MongoHook(mongo_conn_id="mongo_default")
    print("load: connecting via MongoHook(mongo_default)")
    coll = hook.get_collection("example_load", mongo_db="analytics")
    coll.delete_many({})
    coll.insert_many([{"name": f"cat_{i}", "score": (i * 7) % 100} for i in range(20)])
    print(f"load: {coll.count_documents({})} docs in example_load")


with DAG("mongo_load", schedule=None, catchup=False, tags=["example"]):
    load()
```

```yaml
# leoflow.yaml
schema_version: "1.0"
dag_id: mongo_load
description: Load documents into MongoDB via MongoHook.
owner: examples
tags: [example]
python_version: "3.11"
connectors:
  - mongo
```

### Atlas (mongodb+srv)

For MongoDB Atlas set `Extra: {"srv": true}` and use the cluster host
(`cluster0.xxxx.mongodb.net`) without a port — `MongoHook` builds the
`mongodb+srv://` URI.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
