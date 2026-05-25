# Installation

One command installs Leoflow and bootstraps everything it needs — **no sudo, no
system Python, no package manager**:

```bash
curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/install.sh | sh
```

That script downloads the release archive for your OS/architecture, verifies its
SHA-256 against the signed checksums, installs the binaries to `~/.leoflow/bin`,
and then runs [`leoflow setup`](#what-leoflow-setup-does).

!!! warning "Pre-alpha builds expire"
    Pre-alpha binaries carry a baked-in expiry (~90 days) and refuse to run past
    it — `leoflow version` shows `[expires …]`. This is intentional: pre-alpha
    builds are not durable. When one expires, re-run the install command for a
    fresh build. (`LEOFLOW_IGNORE_EXPIRY=1` overrides in a pinch.)

## What you need

Almost nothing. The control plane, CLI, and agent are **static Go binaries**, and
`leoflow setup` provisions a Python 3.11 itself if you don't have one. The only
things that unlock higher tiers are Docker and a cluster — and those are optional:

| Tier | Needs | For |
|---|---|---|
| **0 — subprocess** | just the install (binaries + a managed Python) | small projects, zero Docker/k8s |
| **1 — docker** | + Docker | pod-per-task isolation, no Kubernetes |
| **2 — k8s** | + Docker (k3d/kubectl fetched on demand) | a local Kubernetes executor |

`leoflow setup` **detects what's present and picks the highest tier available**;
without Docker it falls back to the subprocess tier. Run
[`leoflow doctor`](#leoflow-doctor) anytime to see where you stand.

## What `leoflow setup` does

`setup` is idempotent — re-running is safe. It:

1. **Ensures Python 3.11.** Uses a system `python3.11` if one is on `PATH`;
   otherwise downloads a pinned, checksum-verified [relocatable
   CPython](https://github.com/astral-sh/python-build-standalone) into
   `~/.leoflow/python`. No sudo, no system install.
2. **Extracts the DAG parser and task runtime** (embedded in the binary) to
   `~/.leoflow/pysrc`.
3. **Provisions the parser venv** at `~/.leoflow/parser-venv` (installs the parser
   and Apache Airflow — this is the one heavy step, and it runs **once**, then is
   cached), and points `parser_cmd` at it in `~/.leoflow/config.yaml`.
4. **Creates your workspace** (default `~/leoflow`, override with `--workspace`)
   for your DAG projects.

Everything Leoflow manages lives under `~/.leoflow`; your DAG source lives in the
workspace — the two are kept separate.

```bash
leoflow setup                      # bootstrap (prompts nothing; safe to re-run)
leoflow setup --dry-run            # show the plan, change nothing
leoflow setup --workspace ~/work   # choose where your DAG projects live
leoflow setup --skip-python-deps   # binaries + Python only (e.g. parsing in containers)
```

!!! note "There is no scanned `dags/` folder"
    Unlike Airflow, Leoflow has no monolithic DAGs directory. Each DAG is its own
    project (`dag.py` + `leoflow.yaml`); you point `leoflow dev <path>` at it. The
    workspace is just a convenient home for those projects.

## Platforms

Leoflow ships **Linux and macOS** binaries for **amd64 and arm64**. Because the
install never touches your system package manager, the Linux **distribution does
not matter** — only the C library and CPU architecture do:

- **glibc distros** (Ubuntu, Debian, Fedora, RHEL/Rocky/Alma, Arch, openSUSE) and
  **musl** (Alpine) are both supported; `setup` detects musl and fetches the
  matching CPython build.
- **Windows:** use **WSL2** (it's a glibc Linux). Keep your project in the WSL
  **native filesystem** (`~/...`), not under `/mnt/c` — `leoflow dev`'s hot-reload
  uses inotify, which is unreliable on the Windows 9p mount. `leoflow doctor`
  warns when your project is under `/mnt`.

## Verifying the download

The release publishes `checksums.txt` (SHA-256), and the checksums file is
**cosign-signed** (keyless). `install.sh` verifies the archive checksum
automatically. To verify the signature yourself:

```bash
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/neochaotic/leoflow' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

## `leoflow doctor`

A read-only diagnostic — it changes nothing:

```console
$ leoflow doctor
leoflow doctor

  platform      linux/amd64 (glibc)
  python 3.11   found (/usr/bin/python3.11)
  docker        found
  k3d           not found (fetched on demand for the k8s tier)
  kubectl       not found (fetched on demand for the k8s tier)

  recommended tier: k8s
    tier 0 subprocess  always available
    tier 1 docker      available (Docker present; k3d/kubectl fetched on demand)
    tier 2 k8s         available (Docker present; k3d/kubectl fetched on demand)

  next: run `leoflow setup` to bootstrap the managed runtime.
```

## Installer options

| Variable | Effect |
|---|---|
| `LEOFLOW_VERSION=v0.0.1-prealpha.1` | install a specific release (default: newest, including pre-releases) |
| `LEOFLOW_NO_SETUP=1` | install binaries only; run `leoflow setup` yourself later |
| `LEOFLOW_INSTALL_DIR=~/.leoflow/bin` | where to put the binaries |

## Building from source

If you have a Go toolchain and prefer to build it yourself:

```bash
go install github.com/neochaotic/leoflow/cmd/leoflow@latest
go install github.com/neochaotic/leoflow/cmd/leoflow-server@latest
go install github.com/neochaotic/leoflow/cmd/leoflow-agent@latest
# ensure $(go env GOPATH)/bin is on your PATH, then:
leoflow setup
```

A source build is not stamped with an expiry, so it never expires.

## Uninstalling

Everything is self-contained:

```bash
rm -rf ~/.leoflow      # binaries, managed Python, parser venv, config
rm -rf ~/leoflow       # your workspace (only if you want to remove your DAGs too)
```

## Next

- [Quickstart](quickstart.md) — run your first DAG.
- [The `leoflow dev` workflow](dev-workflow.md) — the hot-reload inner loop.
- [Operating modes](operating-modes.md) — Demo · Dev · Production.
