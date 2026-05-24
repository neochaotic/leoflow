# Quickstart

Get a DAG running locally in a few minutes. **Dev-only** today (Production is
[coming soon](operating-modes.md)).

## 1 · Install
```bash
# Homebrew (macOS/Linux) — coming with the first release; until then:
go install github.com/neochaotic/leoflow/cmd/leoflow@latest
go install github.com/neochaotic/leoflow/cmd/leoflow-server@latest
go install github.com/neochaotic/leoflow/cmd/leoflow-agent@latest
# ensure $(go env GOPATH)/bin is on your PATH
```
You also need **Docker** running, and **k3d** + **kubectl** for the default
cluster mode (`brew install k3d kubectl`).

## 2 · Prepare the machine
```bash
leoflow dev setup            # checks deps, builds the base image, provisions the dev DB
```

## 3 · Create and run a DAG
```bash
leoflow init dags/my_pipeline      # scaffold dag.py + leoflow.yaml
leoflow dev dags/my_pipeline       # hot-reload; UI at http://localhost:8088 (marked DEV)
```
Edit `dags/my_pipeline/dag.py`, save, and watch it reload. Trigger the DAG from
the UI to see real pods run.

!!! tip "Faster inner loop"
    `leoflow dev --executor=subprocess dags/my_pipeline` skips the image build and
    runs tasks on the host venv (unsandboxed) — instant reloads.

## Next
- [DAG authoring](dag-authoring.md) — the dialect, `leoflow.yaml`, overrides.
- [CI/CD & deploy examples](deploy.md) — ship it.
- [Operating modes](operating-modes.md) — Demo · Dev · Production.
