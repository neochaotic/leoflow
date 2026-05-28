# Upgrading Leoflow

This page is the canonical answer to "I'm on pre-alpha .N and want to install
.N+1 (or the next alpha) — what happens to my state?"

!!! warning "Pre-alpha"
    Leoflow is **pre-alpha**. The upgrade contract below is honored by the Lite
    edition; we test it on every release. We will not knowingly ship a release
    that breaks it without a clear migration note. We have not yet promised
    forward/backward compatibility across major versions — that is a v1
    concern.

## Lite — what is preserved across upgrades

Reinstalling (running the new `install.sh`, or `brew upgrade leoflow` once
that ships) over an existing Lite install **preserves all of these by
default**:

| What | Where | Notes |
|---|---|---|
| **Workspace** | The path under `workspace:` in `~/.leoflow/config.yaml` (default `~/leoflow-projects`) | Your `dag.py`, `leoflow.yaml`, and any other project files. The installer does not touch this directory. |
| **Datastore** | `~/.leoflow/managed-postgres/data/` (managed Postgres) **or** the `leoflow-data-*` Docker volume (Docker Postgres) | Includes DAG history, runs, task instances, XCom, Variables, Connections. The new binary applies any pending SQL migrations on first start. |
| **Admin login** | `~/.leoflow/config.yaml` (`admin_email`, `admin_password_hash`) | Your password is not regenerated. Use `leoflow lite reset-password` if you forgot it. |
| **JWT signing secret** | `~/.leoflow/config.yaml` (`jwt_secret`) | Browser sessions survive the upgrade (no forced re-login). |
| **Parser + runtime venv** | `~/.leoflow/venv/` | Project dependencies are reinstalled lazily as needed (the marker at `~/.leoflow/venv/.leoflow-deps` triggers a refresh when the project's deps change). |

## What changes

| What | Why |
|---|---|
| The `leoflow` / `leoflow-server` / `leoflow-agent` binaries on `PATH` | Replaced by `install.sh`. The pre-alpha expiry on the previous build is what nudged you to upgrade. |
| `~/.leoflow/python/` (managed CPython) | Pinned per release; replaced if the new release pins a different version. |
| The SQL schema | The new binary applies any missing migrations on first start. |

## Drift detection

If you somehow run an **older** `leoflow` binary against a database a **newer**
binary has already migrated, the older binary refuses to start with:

```
database is at schema version 18 but this binary only knows up to 15;
an older `leoflow` is being run against a newer database.
Upgrade the binary, or run `leoflow uninstall --purge` to start over
(this WIPES your data)
```

This is the safe behavior: continuing with a stale schema would corrupt
rows the older binary does not understand. Upgrade, or wipe — never both.

## Fresh start

If you want a clean slate without the prior history:

```sh
leoflow uninstall --purge
```

`--purge` removes the binaries, `~/.leoflow/` (config + datastore + parser
sources), and the workspace directory. Without `--purge`, uninstall keeps the
datastore and workspace so a future reinstall picks up where you left off
(this is also the contract upgrades rely on).

## How to test an upgrade safely (recommended)

Before installing a new pre-alpha tag on a Lite install you depend on:

1. **Back up first.** See [Backup and restore](backup-restore.md) (TODO — see
   issue #137). Until that command lands, capture the datastore manually:
   ```sh
   # managed-postgres install:
   tar -czf leoflow-backup-$(date +%F).tar.gz \
       ~/.leoflow/config.yaml \
       ~/.leoflow/setup.json \
       ~/.leoflow/managed-postgres/data \
       ~/leoflow-projects   # or your workspace dir
   ```
2. Install the new version. The drift detector protects you from the worst
   downgrade case.
3. If anything looks off, restore from the tarball.

## Production (Pro) — coming soon

The Helm chart's upgrade story is being built alongside the chart hardening
(PR #96). The shape will be the standard Helm `upgrade --install` with the
migration Job ensuring schema parity, but the doc is not final yet — track
issue #141.

## Related issues

- #136 — this contract.
- #137 — `leoflow lite backup` / `restore` commands.
- #60 / #61 — embed migrations + single binary (Lite distribution shape).
