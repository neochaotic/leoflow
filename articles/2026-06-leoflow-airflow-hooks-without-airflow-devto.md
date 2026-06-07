---
title: "Your Airflow PostgresHook just ran on Leoflow — and we never installed Airflow"
published: false
description: "How Leoflow runs real apache-airflow-providers-* hooks with a ~340-line shim and a --no-deps install, so migrating DAGs keep their connectors without dragging in the 300 MB Python control plane. Headline feature of v0.1.0-rc.1."
tags: airflow, go, python, dataengineering
cover_image: https://raw.githubusercontent.com/neochaotic/leoflow/main/docs/assets/screenshots/etl-graph.png
series: "Building Leoflow"
---

> **TL;DR** — Leoflow rewrote Airflow's control plane in Go. The thing that keeps people *on* Airflow isn't the scheduler, though — it's the **provider hooks**: thirty years of `from airflow.providers.postgres.hooks.postgres import PostgresHook`. So we made those run on Leoflow **without installing apache-airflow**. A ~340-line shim provides the `airflow.sdk` surface the hooks import; the provider wheel is installed `--no-deps`; the hook resolves its connection through Leoflow's own seam. This lands in **`v0.1.0-rc.1`**. GitHub: **[neochaotic/leoflow](https://github.com/neochaotic/leoflow)**.

---

## The connector is the lock-in

Last time ([we rewrote Airflow's control plane in Go](https://github.com/neochaotic/leoflow)) the comments were warm — and almost all of them had the same shape:

> "Cool. But my DAGs use `PostgresHook`, `S3Hook`, `SnowflakeHook`… am I rewriting all of that?"

Fair. The scheduler is what *hurts* in Airflow, but the **provider catalog** is what *keeps you there*. `apache-airflow-providers-*` is the accreted plumbing of a decade: every database, every cloud, every SaaS, all behind the same `Hook.get_connection()` / `Hook.get_records()` muscle memory. Tell a data team "great news, you get to re-implement all your connectors" and the migration ends right there.

So the real question for any Airflow replacement isn't "is your scheduler faster." It's: **can my existing hook code run, unchanged?**

On Leoflow `v0.1.0-rc.1`, the answer is yes. And the way it works is the fun part.

---

## The thing nobody tells you about provider hooks

A provider hook looks like it needs Airflow. It imports from `airflow.*`, it subclasses Airflow base classes, its wheel literally declares `Requires: apache-airflow`.

But watch what `PostgresHook` actually *does* at runtime:

```python
from airflow.providers.postgres.hooks.postgres import PostgresHook

hook = PostgresHook(postgres_conn_id="sales_db")
hook.get_records("SELECT count(*) FROM orders")
```

It calls `BaseHook.get_connection("sales_db")`, gets back a `Connection` object (host, login, password, extra), and opens a `psycopg2` socket. That's it. It does **not** need a scheduler, a `DagBag`, a metadata DB, a triggerer, or 600 transitive Python dependencies. It needs:

1. The `airflow.sdk.*` import surface to *resolve*.
2. A `Connection` to come back from `get_connection`.
3. `psycopg2` to actually talk to Postgres.

Airflow ships all three welded into a 200–300 MB monolith. Leoflow ships **only the first two** — as a shim — and lets you `pip install` the third.

Here's the whole journey of one connection, from the admin form to a real query:

```text
   Admin UI  (Go control plane)              DAG image  (built once per push)
   ┌────────────────────────────┐            ┌─────────────────────────────────┐
   │ POST /api/v2/connections   │            │  apache-airflow-providers-       │
   │   conn_id = "sales_db"     │            │    postgres        (--no-deps)   │
   │   host · login · password  │            │  psycopg2-binary                 │
   │   extra (JSON)             │            │  leoflow compat shim  (~340 LOC) │
   └─────────────┬──────────────┘            └────────────────┬────────────────┘
                 │ encrypt at rest (AES-256-GCM)              │
                 ▼                                            │
   ┌────────────────────────────┐                            │ task starts
   │ Leoflow agent (Go)         │   on dispatch              ▼
   │   renders the wire URI:     │ ─────────────►  ┌─────────────────────────────┐
   │   AIRFLOW_CONN_SALES_DB=    │  inject env var │ PostgresHook("sales_db")    │
   │   postgres://u:p@host:5432… │                 │  └ BaseHook.get_connection()│
   └────────────────────────────┘                 │      └ reads AIRFLOW_CONN_*  │ ← the shim
                                                   │  psycopg2.connect(...)      │
                                                   └──────────────┬──────────────┘
                                                                  │ real query
                                                                  ▼
                                                            your Postgres
```

Connection **metadata** is owned by Leoflow (Go, encrypted). Hook **code** is owned by the provider wheel (Python, BYO). They meet at exactly one place: the `AIRFLOW_CONN_*` env var. No Airflow in sight.

---

## What "the shim" actually is

It's a Python package that **is** `airflow`. ~340 lines across a handful of files, kilobytes on disk, dormant until a provider import triggers it. It mirrors exactly the slice of Airflow 3.2's SDK that real provider wheels import:

```
airflow/
├── __init__.py                      # namespace pkg + __version__ = "3.2"
├── exceptions.py                    # AirflowException, ...ProviderDeprecationWarning, ...
├── configuration.py                 # a no-config `conf` that returns your fallback
├── providers_manager.py             # a no-op ProvidersManager
├── sdk/
│   ├── bases/hook.py                # BaseHook.get_connection  ← the seam
│   └── definitions/{connection,variable}.py
└── utils/
    ├── module_loading.py            # import_string / qualname / iter_namespace
    └── log/logging_mixin.py         # LoggingMixin → self.log
```

The keystone is the connection seam. Real Airflow walks a secrets-backend chain to find a connection. Leoflow's delivery is single-source — the agent injects `AIRFLOW_CONN_<ID>` into the task — so the shim just reads it:

```python
class BaseHook:
    @classmethod
    def get_connection(cls, conn_id: str) -> Connection:
        uri = os.environ.get("AIRFLOW_CONN_" + conn_id.upper().replace("-", "_"))
        if uri is None:
            raise AirflowNotFoundException(f"The conn_id `{conn_id}` isn't defined")
        return Connection(conn_id=conn_id, uri=uri)
```

That URI format — `postgres://login:password@host:5432/db?__extra__={...}` — is byte-for-byte what Airflow's own env-secrets backend uses. The connection your admin created in the Leoflow UI (encrypted at rest, AES-256-GCM) is rendered into that env var by the Go agent at dispatch. The upstream hook never knows the difference.

---

## Two files, your existing hook

From the DAG author's side, nothing exotic. Declare the provider in `leoflow.yaml`:

```yaml
# leoflow.yaml
dag_id: nightly_rollup
python_version: "3.11"
dependencies:
  - apache-airflow-providers-postgres>=6.0
  - psycopg2-binary>=2.9
```

…and write the same hook code you already have:

```python
# dag.py
from airflow.sdk import DAG, task
from airflow.providers.postgres.hooks.postgres import PostgresHook

@task
def rollup():
    hook = PostgresHook(postgres_conn_id="sales_db")
    rows = hook.get_records("SELECT date, sum(amount) FROM orders GROUP BY 1")
    return [{"date": str(d), "total": float(t)} for d, t in rows]

with DAG("nightly_rollup", schedule="0 5 * * *", catchup=False):
    rollup()
```

`leoflow compile` builds the image, installs the provider `--no-deps` (so `apache-airflow` is never pulled), adds the provider's *real* third-party deps, and bakes the shim in. The hook resolves `airflow.*` against the shim. Done.

---

## The proof (the only part that matters)

Here's a real `apache-airflow-providers-postgres` `PostgresHook`, in an environment where **`apache-airflow` is not installed at all**, resolving a Leoflow connection through the shim:

```text
$ pip show apache-airflow
WARNING: Package(s) not found: apache-airflow        # ← it's genuinely not here

$ AIRFLOW_CONN_SALES_DB='postgres://u:p@dbhost:5432/sales?__extra__=%7B%22sslmode%22%3A%22require%22%7D' \
  python -c "
from airflow.providers.postgres.hooks.postgres import PostgresHook
h = PostgresHook(postgres_conn_id='sales_db')
c = h.get_connection('sales_db')
print(c.conn_type, c.host, c.port, c.schema, c.extra_dejson)
print(h.get_uri())
"
postgres dbhost 5432 sales {'sslmode': 'require'}
postgresql://u:p@dbhost:5432/sales
```

The hook imported, resolved the connection through Leoflow's `BaseHook`, parsed every field, and produced a valid SQLAlchemy URI — with the 300 MB control plane nowhere on the machine.

### The image diet (measured, not hand-waved)

Here's the part that surprised even us. To author DAGs, a task image only needs the `airflow.sdk` import surface — `from airflow.sdk import DAG, task`. The usual way to get that is `apache-airflow-task-sdk`, which sounds lightweight. It isn't: its dependency metadata pulls in `apache-airflow-core`, FastAPI, SQLAlchemy, the providers bundle — the works.

We measured it. A clean venv with **only** `pip install apache-airflow-task-sdk==1.2.1`:

```text
$ du -sh site-packages                 # task-sdk + everything it dragged in
260M    site-packages
$ pip list | grep -i airflow
apache-airflow            3.2.1         # ← "task SDK" quietly pulled full core
apache-airflow-core       3.2.1
apache-airflow-task-sdk   1.2.1
...
```

Leoflow ships its own `airflow.sdk` instead — a pure-Python shim that provides the exact surface DAGs and provider hooks import, and nothing else:

```text
$ du -sh site-packages/airflow          # the Leoflow compat shim
160K    site-packages/airflow
```

So the `airflow` surface in a task image goes from **260 MB → 0.16 MB** — about **1,600× smaller** — for the same `from airflow.sdk import DAG, task` and the same provider hooks:

```text
   apache-airflow-task-sdk            Leoflow compat shim
   ─────────────────────              ───────────────────
   ████████████████████  260 MB       ▏ 0.16 MB
   (pulls apache-airflow-core,        (pure Python: airflow.sdk surface
    FastAPI, SQLAlchemy, providers)    + the AIRFLOW_CONN_* connection seam)
```

All 20 of Leoflow's example DAGs — TaskFlow, fan-in, the SQL/redis/http connectors — import against the shim with **no apache-airflow installed at all**. Same hook, same query, same connection; one of these just stopped shipping a database control plane into a pod that only wanted to run `SELECT count(*)`.

---

## How we actually found the surface (a short tale of engineering honesty)

The original design doc said, confidently: *"vendor the single `DbApiHook` file — ~1,250 lines — and it lights up postgres, mysql, sqlite, mssql, snowflake without re-implementation."*

That aged like milk. The moment we looked at the current `common-sql` release, `DbApiHook` imported a whole subtree — dialects, handlers, lineage, a providers manager — plus `sqlalchemy`, `sqlparse`, `methodtools`, `more_itertools`. "One file" was a fantasy. Worse, the provider wheels declare `Requires: apache-airflow` in their metadata, so a naïve `pip install` drags in exactly the monolith we were trying to avoid.

So we flipped the strategy and let **reality drive the surface**. We built a test venv with the provider installed `--no-deps` (no Airflow), tried to import the hook, and treated each `ImportError` as a to-do list:

```
ImportError: cannot import name '__version__' from 'airflow'        → add airflow.__version__
ModuleNotFoundError: No module named 'airflow.utils.module_loading' → add the helpers
ModuleNotFoundError: No module named 'airflow.utils.log'            → add LoggingMixin
ModuleNotFoundError: No module named 'airflow.providers_manager'    → add the stub
IMPORT OK: PostgresHook                                             → 🎉
```

As a loop, the whole development method was this:

```text
        ┌───────────────────────────────────────────────┐
        │  venv: provider installed --no-deps            │
        │        (apache-airflow NOT present)            │
        └───────────────────────┬───────────────────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │  import PostgresHook   │ ◄─────────────┐
                    └───────────┬───────────┘                │
                                │                            │
                       ┌────────▼────────┐   ImportError:    │
                       │ does it import? │── "name 'X'" ──┐  │
                       └────────┬────────┘                │  │
                                │ yes              ┌──────▼──────────┐
                                ▼                  │ add X to shim   │
                          🎉 IMPORT OK             │ (test-first)    │
                                                   └──────┬──────────┘
                                                          └──────────┘
```

The shim grew to the providers' **actual** access set, never the entire `airflow.sdk.__all__`. Every symbol added is one a real wheel demanded — measured, not guessed. (Every line of it written test-first, because that's the house rule.)

We also learned the hard way that **versions matter**: the latest providers import 3.3-era paths that don't exist in the 3.2 line we target. So the compat surface is pinned to an Airflow *minor*, and a CI matrix installs the providers against the shim on every release to catch drift. That's the honest maintenance cost — a budgeted day or two per Airflow minor — and we'd rather write it down than pretend it's free.

---

## What this is, and what it isn't

**Is:**
- A way to run real `apache-airflow-providers-*` hooks on Leoflow with no `apache-airflow` install.
- Kilobytes of shim vs. 200–300 MB of control plane in every task image.
- Connections still created in the UI, encrypted at rest, delivered as `AIRFLOW_CONN_*` — BYO-hook, Leoflow-managed credentials.

**Isn't:**
- A re-implementation of any connector. The hook code is upstream's; we provide the `airflow.sdk` surface it stands on.
- Magic for *every* provider on day one. `v0.1.0-rc.1` officially covers **postgres, mysql, sqlite, redis, http**, each with a cookbook page and a CI gate. Others may already work; we only claim what the matrix proves.
- Done. This is a release **candidate** — pre-1.0, surface may still move ([ADR 0037](https://github.com/neochaotic/leoflow/blob/main/docs/adr/0037-release-version-scheme.md)).

---

## Try it / break it

```bash
curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/install.sh | sh
leoflow lite                 # http://localhost:8088
```

Add a Postgres connection in the UI, drop the DAG above in your workspace, and watch your existing hook code run on a Go control plane.

If you have a provider we don't cover yet, **that's the issue we most want**: it tells us exactly which symbol to add next. The shim is demand-driven by design.

- ⭐ **Star** [github.com/neochaotic/leoflow](https://github.com/neochaotic/leoflow)
- 🐘 **Tell us which provider you need** — open an issue with the `from airflow.providers...` import line
- 🛠️ **PR a connector cookbook** — strict TDD, fast reviews ([CONTRIBUTING](https://github.com/neochaotic/leoflow/blob/main/CONTRIBUTING.md))

If you've ever waited five seconds for an Airflow task to start *and* sworn you'd never rewrite your hooks — this release is for you.

**→ [github.com/neochaotic/leoflow](https://github.com/neochaotic/leoflow)**

Thanks for reading. Tell us which hook to light up next.
