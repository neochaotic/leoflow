# The Lite web editor

Leoflow **Lite** ships a small built-in code editor so you can edit a DAG project
straight from the browser — no separate IDE, no extra process. It is a
**Lite-only** convenience for the local, single-machine workflow; **Production**
teams author DAGs in their own editor and ship them through the GitOps flow.

!!! info "Why it exists, and what it is not"
    The editor is a thin **[Monaco](https://microsoft.github.io/monaco-editor/)**
    surface (the engine behind VS Code) with a file tree — enough to open, edit,
    save, create, and delete files in your workspace, with **syntax highlighting
    for Python and YAML**. It is intentionally *not* a full IDE: there are no
    extensions, no integrated terminal, and no debugger. For those, keep using
    your own editor against the same workspace folder. See
    [ADR 0025](adr/0025-lite-embedded-web-editor.md) for the rationale.

## Opening it

When you run `leoflow lite <project>`, the served UI shows a small **⌨ IDE**
button in the bottom-right corner. Click it — the editor opens in a **new tab**
at `/ide`, scoped to your project workspace.

You can also open it directly at `http://localhost:8088/ide` (or your Lite host
and port).

The editor follows your OS light/dark preference. Force a theme with a query
parameter:

```text
/ide?theme=light
/ide?theme=dark
```

## What it can do

| Action | How |
|---|---|
| Browse the workspace | The left panel lists every file and folder (skipping `.git`, `__pycache__`, `node_modules`, …). |
| Open & edit a file | Click it in the tree; edits happen in the Monaco editor with Python/YAML highlighting. |
| Save | **⌘S / Ctrl-S**, or the **Save** button. |
| Create a file | **New file** → type a path relative to the workspace (e.g. `tasks/extract.py`). |
| Delete | **Delete** removes the open file. |

Saving a file is exactly like editing it on disk — the `leoflow lite` watcher
picks up the change and **hot-reloads** the DAG, same as if you had saved from any
editor. (Remember the [reload gotcha](dev-workflow.md#the-edit-reload-see-it-cycle):
the open Airflow tab does not auto-refresh DAG *structure* — reload it.)

## Provisioning the editor assets

To keep the binary light, the Monaco bundle (~13 MB) is **not** baked into the
`leoflow` binary. It is downloaded **once**, pinned and SHA-256-verified, by:

```bash
leoflow setup            # end-user install
# or, for the from-source contributor loop:
leoflow lite provision
```

into `~/.leoflow/assets/monaco/<version>/`. After that first fetch the editor
works **fully offline**.

If the assets are not present yet (for example, an offline install), the `/ide`
page shows a short hint to run `leoflow setup` instead of a broken screen. The
rest of Lite — scheduling, runs, the API — is unaffected.

## Security

The editor's file API reads and writes **only inside the configured workspace**:
every path is confined to the workspace root, and absolute paths or `..` traversal
are rejected. Like the rest of Lite, it is meant for a **local / internal-network
or VPN** deployment — see the
[Lite security note](editions.md). It is gated to the Lite edition and is never
registered in Production.
