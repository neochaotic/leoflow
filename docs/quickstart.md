# Quickstart

Get a DAG running locally in a few minutes. **Dev-only** today (Production is
[coming soon](operating-modes.md)).

## 1 · Install
```bash
curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/install.sh | sh
```
This installs the binaries and runs `leoflow setup` (ensures Python, provisions
the parser, creates your workspace) — **no sudo, no system Python**. Docker is
optional and only unlocks the higher [tiers](installation.md#what-you-need); the
subprocess tier needs nothing else. See [Installation](installation.md) for
platforms (incl. WSL2) and verification.

## 2 · Prepare the machine
```bash
leoflow doctor               # see your platform, deps, and achievable tier
leoflow lite provision            # builds the base image, provisions the dev DB
```

## 3 · Create and run a DAG
```bash
leoflow init dags/my_pipeline      # scaffold dag.py + leoflow.yaml
leoflow lite dags/my_pipeline       # hot-reload; UI at http://localhost:8088 (marked LITE)
```
Edit `dags/my_pipeline/dag.py`, save, and watch it reload. Trigger the DAG from
the UI to see real pods run.

!!! tip "Faster inner loop"
    `leoflow lite --executor=subprocess dags/my_pipeline` skips the image build and
    runs tasks on the host venv (unsandboxed) — instant reloads.

## Next
- [DAG authoring](dag-authoring.md) — the dialect, `leoflow.yaml`, overrides.
- [CI/CD & deploy examples](deploy.md) — ship it.
- [Editions](editions.md) — Lite (now) vs Production (coming).
