---
title: Upgrades
weight: 40
description: "Upgrade a Leoflow control plane safely, edition by edition."
---

This page is the canonical answer to "I'm on `v0.x.y` and want to install a
newer tag — what happens to my state?"

{{% alert title="Upgrade contract" color="info" %}}
The upgrade contract below is honored by the Lite edition; we test it on
every release. We will not knowingly ship a release that breaks it without a
clear migration note. On the `v0.x` line we do not yet promise
forward/backward compatibility across major versions — that is a v1
concern.
{{% /alert %}}

## Lite — what is preserved across upgrades

Reinstalling (running the new `install.sh`, or `brew upgrade leoflow` once
that ships) over an existing Lite install **preserves all of these by
default**:

| What | Where | Notes |
|---|---|---|
| **Workspace** | The path under `workspace:` in `~/.leoflow/config.yaml` (default `~/leoflow`) | Your `dag.py`, `leoflow.yaml`, and any other project files. The installer does not touch this directory. |
| **Datastore** | `~/.leoflow/managed-postgres/data/` (managed Postgres) **or** the `leoflow-data-*` Docker volume (Docker Postgres) | Includes DAG history, runs, task instances, XCom, Variables, Connections. The new binary applies any pending SQL migrations on first start. |
| **Admin login** | `~/.leoflow/config.yaml` (`admin_email`, `admin_password_hash`) | Your password is not regenerated. Use `leoflow lite reset-password` if you forgot it. |
| **JWT signing secret** | `~/.leoflow/config.yaml` (`jwt_secret`) | Browser sessions survive the upgrade (no forced re-login). |
| **Parser + runtime venv** | `~/.leoflow/venv/` | Project dependencies are reinstalled lazily as needed (the marker at `~/.leoflow/venv/.leoflow-deps` triggers a refresh when the project's deps change). |

## What changes

| What | Why |
|---|---|
| The `leoflow` / `leoflow-server` / `leoflow-agent` binaries on `PATH` | Replaced by `install.sh`. |
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

Before installing a newer tag on a Lite install you depend on:

1. **Back up first.** See [Backup and restore](/operate/backup-restore/):
   ```sh
   leoflow lite backup --output ~/snap-before-upgrade.tar.gz
   ```
2. Install the new version. The drift detector protects you from the worst
   downgrade case.
3. If anything looks off, restore from the tarball.

## Pro — upgrade path

The Pro control plane upgrades with the standard Helm flow: re-run
`helm upgrade` against the same release, pointing the pinned image tag at the
newer version.

```sh
# OCI chart (the primary install path — see Installation):
helm upgrade leoflow oci://ghcr.io/neochaotic/charts/leoflow --version <VERSION> \
  -n leoflow --reuse-values

# Or pin the image tags explicitly:
helm upgrade leoflow oci://ghcr.io/neochaotic/charts/leoflow --version <VERSION> \
  -n leoflow --reuse-values \
  --set image.tag=<VERSION> \
  --set migrations.image.tag=<VERSION>
```

The chart runs a **pre-upgrade migrations Job** (`golang-migrate` against
`database.url`) before the new `leoflow-server` rolls out, so the schema is
brought to parity before any new binary serves traffic. The same startup
**drift detector** described above protects a Pro control plane from being run
against a database a newer binary already migrated. Use `--version <VERSION>`
with the chart version — the [latest release](https://github.com/neochaotic/leoflow/releases)
tag with the leading `v` stripped.

## Related issues

- #136 — this contract.
- #137 — `leoflow lite backup` / `restore` commands.
- #60 / #61 — embed migrations + single binary (Lite distribution shape).
