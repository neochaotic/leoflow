# PoC: stdlib-only Airflow shim for the DAG parser (issue #83)

Proof of concept that the Leoflow DAG parser can run **without depending on Apache
Airflow** by exec'ing the user's `dag.py` against a tiny structural *shim* of
`airflow` — a stand-in package that provides just the classes the DAG imports
(`airflow.sdk.DAG`, `@task`, `BashOperator`, `HttpOperator`, `PythonOperator`,
`EmptyOperator`) and records structure as the file runs.

It reproduces exactly the surface `parser/leoflow_parser/compiler.py` reads, and
**unsupported operators raise a clear error** (their module isn't in the shim, so
the import fails fast with "Leoflow does not support …").

## Why

| | Today (real Airflow) | This shim |
|---|---|---|
| Parser env size | **262 MB** | **~44 KB** |
| Third-party deps | **136** | **0** (stdlib only) |
| Parse a DAG | needs `pip install apache-airflow` | pure Python, instant |

The 262 MB is `apache-airflow-core`'s transitive tree (grpc, sqlalchemy, babel,
cryptography, pydantic, …) — none of which the parser uses; it only constructs
DAG/operator objects and reads attributes.

## Scope (deliberate)

- **Parser only.** The runtime keeps the real Task SDK so user task code that uses
  `airflow.sdk` helpers at execution time still works (see #83 discussion).
- Covers the supported operators: Bash, Http, Python / TaskFlow `@task`. Anything
  else is an explicit "unsupported" error — by design.

## Run it

```bash
cd experiments/parser-shim
python3 extract.py ../../examples/taskflow_sales/dag.py   # prints the compiled structure
python3 -m pytest test_examples.py -q                     # all 12 examples + edge cases
```

## Status

PoC validated: 15/15 checks pass against the 12 shipped examples (structure +
TaskFlow XCom edges + unsupported-operator error). Not wired into the real
compiler yet — see #83 for the productization plan (fidelity tests, replace
`DagBag`, then drop the parser venv and embed the parser as pure Python).
