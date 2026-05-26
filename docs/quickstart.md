# Quickstart

Get Leoflow **Lite** running locally in two commands. Lite is the local edition
(Production is [coming soon](operating-modes.md)); run it on your machine or a
trusted internal network — see [Editions](editions.md).

## Prerequisites

- **Docker** running — Lite brings up its own Postgres + Redis with it. (No
  Docker? Point Lite at your own datastores with `--no-up`.)
- Linux or macOS (incl. WSL2). No system Python needed — Lite installs a managed
  one. See [Installation](installation.md) for details.

## 1 · Install

```bash
curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/install.sh | sh
```

This installs the binaries to `~/.leoflow/bin`, **adds them to your PATH**, and
runs `leoflow setup` — which fetches a managed Python + the editor, creates your
workspace (`~/leoflow`), and prints your **admin password once**:

```
── Leoflow Lite admin (save this — it is shown only once) ──
  user:     admin@leoflow.local
  password: tiger98
```

Save that password. Then load the new PATH (or open a new terminal):

```bash
source ~/.bashrc        # or ~/.zshrc
```

## 2 · Run it

```bash
leoflow lite
```

That's it. With no arguments, `leoflow lite`:

1. scaffolds a starter DAG in your workspace (if it has none yet),
2. brings up Postgres + Redis (Docker),
3. starts the control plane and prints where to go:

```
✓ Leoflow Lite is ready
    open:    http://127.0.0.1:8088
    login:   admin@leoflow.local
    project: /home/you/leoflow
```

Open that URL, log in with the password from step 1, and the DAG shows up in the
Airflow-compatible UI. `leoflow lite` keeps running and **hot-reloads** on every
save — press Ctrl-C to stop.

!!! tip "Open it from another machine (internal network / VPN)"
    Lite binds loopback by default. To reach it from your LAN, add `--host
    0.0.0.0` — the banner then prints your machine's IP
    (`http://192.168.x.y:8088`). It is honored only with a login configured, and
    Lite is for trusted networks only — never the public internet.

## 3 · Edit your DAG

Click the **`< >` IDE** button (bottom-right of the UI) to open the built-in web
editor — file tree + Python/YAML highlighting. Edit `dag.py`, save, and the DAG
reloads in a couple of seconds. (Reload the browser tab to see structure
changes; run state auto-refreshes.) Details: [the Lite web editor](lite-web-editor.md).

## Useful commands

```bash
leoflow doctor                       # check platform, deps, and what's achievable
leoflow lite --host 0.0.0.0          # reachable from your internal network
sudo leoflow lite reset-password     # set a new admin password (after first run)
leoflow uninstall                    # remove the install (--purge for workspace + volumes)
```

## Next

- [DAG authoring](dag-authoring.md) — the dialect, `leoflow.yaml`, overrides.
- [The `leoflow lite` workflow](dev-workflow.md) — the edit→reload loop, executors.
- [CI/CD & deploy examples](deploy.md) — ship it.
- [Editions](editions.md) — Lite (now) vs Production (coming).
