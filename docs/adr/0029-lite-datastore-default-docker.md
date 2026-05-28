# ADR 0029: Lite Datastore Default — Docker Postgres (Managed PG is the Opt-In)

**Status:** Superseded by [ADR 0030](0030-lite-datastore-auto-select.md)
**Date:** 2026-05-27
**Deciders:** Project founder
**Refines:** ADR 0027 (Product Editions), ADR 0026 (Lite Datastore — XCom on Postgres)
**Relates:** ADR 0015 (Kubernetes as the sole container execution path)

> **Superseded (2026-05-27):** this ADR made Docker the default and the managed PG
> an explicit opt-in flag. ADR 0030 keeps both backends but makes the choice
> **automatic** (`--postgres auto`): Docker Postgres when Docker is present, the
> managed relocatable PG when it is absent — so the Docker-free path is the default
> behavior on a Docker-free host, not a hidden flag. See
> [ADR 0030](0030-lite-datastore-auto-select.md).

## Context

Fase 2 / prealpha.16 promoted the **managed relocatable Postgres** (a theseus-rs
build under `~/.leoflow`, on a Unix socket) to Lite's **default** datastore, for a
"Docker-free out of the box" story (ADR 0027 described Lite's datastore as
"embedded managed Postgres, no Docker").

Testing the default on real and minimal hosts exposed a portability problem: the
relocatable build **dynamically links a chain of system libraries it does not
bundle** — ICU, Kerberos/GSSAPI, zstd, lz4, libxml2, … . On full distros (Ubuntu,
Fedora, …) these are present, so it works; on **minimal hosts (Alpine/musl, slim
containers)** they are absent and the server dies on a loader error. Installing a
couple of packages is not enough (the chain is ~a dozen libs, version-matched);
the proper fix (bundle the chain or a lean PG build) is non-trivial (#97).

Two facts make Docker the better default:

1. Lite's **default executor is k3d**, which already **requires Docker**. So a
   typical `leoflow lite` user already has Docker — and where Docker exists, the
   Docker Postgres (`postgres:16`) "just works" (its lib chain is baked into the
   image by apk at build time).
2. The "Docker-free out of the box" promise was only **partial** anyway, because
   the default executor needed Docker regardless.

## Decision

**Lite's default datastore is Docker Postgres** (`postgres:16` via `docker
compose`). The **managed relocatable Postgres is the opt-in** (`--postgres
managed`) for a Docker-free datastore — recommended on full distros; on minimal
hosts a pre-flight fails loud and points back to `--postgres docker`.

This **does not reintroduce a Docker executor** and does not contradict ADR 0015:
the control plane still imports **no Docker SDK** and runs tasks only via the
Kubernetes (k3d) or subprocess executors. Docker here runs **only a Postgres
container via the `docker` CLI** — the datastore, not task execution.

ADR 0026 is unaffected: Lite remains **Redis-free** with **XCom stored in
Postgres**, whichever Postgres (Docker or managed) it connects to.

## Consequences

- **Robust default:** the default `leoflow lite` works wherever Docker runs (the
  common case, aligned with the k3d executor default) — no relocatable-PG lib
  fragility on the default path.
- **Managed PG stays, hardened:** `--postgres managed` is the Docker-free opt-in,
  with the .17 fixes (locale-independent initdb, socket-only via postgresql.conf,
  socket-path guard, and a `postgres --version` pre-flight that fails loud with a
  clear `--postgres docker` fallback).
- **Self-contained managed runtime (#97) is no longer a default-path blocker** —
  it becomes a quality improvement for the opt-in path.
- **Editions framing updated:** ADR 0027's "Lite datastore: embedded managed, no
  Docker" is refined here to "Docker Postgres by default; managed (relocatable,
  no-Docker) as opt-in." The no-Docker datastore remains achievable, just not the
  default.
