---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0061-secret-locality.html
# --- end AUTO redirect aliases ---
title: "ADR 0061: Secret material never lands in the user's tree — private scratch, masked on read"
linkTitle: "0061 · Secret locality — private scratch, masked on read"
weight: 610
description: "ADR 0061: Secret material is written only to a private, ephemeral scratch (never the project/CWD/repo) and masked on read — the invariant that prevents the credential-leak class."
---

**Status:** Accepted
**Date:** 2026-09-02
**Accepted:** 2026-09-02
**Relates:** ADR 0019 (secret encryption at rest — the vault), ADR 0021 (exposing variables/connections to pods), ADR 0045 (declared secret delivery), ADR 0055 (secret scoping + token liveness), ADR 0060 (external secrets resolution). This ADR is the cross-cutting *locality* invariant those composed decisions all assume but none stated.
**Issues:** #882 (#12, Lite dbt `profiles.yml` written to the project CWD), #11 (connection `extra` echoed on read), GHSA-3r74-9w27-v32f / #828 (author env override) — three instances of one class.

## Context

leoflow keeps secrets in the control-plane vault, encrypted at rest (ADR 0019),
and delivers them to a task pod-side as `AIRFLOW_CONN_<ID>` / `AIRFLOW_VAR_<KEY>`
over the ADR 0055 exchange (ADR 0021/0045/0060). That covers the secret's life
*in transit* and *at rest in the vault*. It does **not** state where a task or a
feature may put a secret once it holds one — and that gap has now produced the
same bug three times:

- **#882 (#12).** A Lite dbt task's profile step defaulted its output dir to the
  process CWD — the dbt project in the user's working tree — so the generated
  `profiles.yml`, carrying the managed connection's secret in clear, overwrote
  the repo's version-controlled `profiles.yml`. One `git add` from committing a
  live credential. (The pod path was safe only because the base image happened to
  set `DBT_PROFILES_DIR` to `/tmp`.)
- **#11.** `GET /api/v2/connections` returned the free-form `extra` verbatim, so
  provider secrets (`client_secret`, `token`, `keyfile_dict`) were echoed to any
  reader — the write-only `password` field was protected, `extra` was not.
- **#828 (GHSA-3r74-9w27-v32f).** A DAG's `env:` could override reserved
  `LEOFLOW_` variables and redirect the in-pod agent's credentials.

Each was fixed in isolation. None of them had to happen: they share a single
missing rule about **where a secret is allowed to be** and **who is allowed to
read it back**.

## Decision

Two invariants, binding on all code — core, runtime, CLI, connectors, and any
future feature:

1. **Private locality.** Secret material — connection URIs, generated
   `profiles.yml`, keyfiles, tokens, any credential-bearing artifact — is written
   **only** to a private, ephemeral, non-committable location: a per-task scratch
   dir (`0700`, created fresh, removed when the task ends). It is **never** written
   to the project directory, the process CWD, the repo, `$HOME` dotfiles a user
   might commit, or any path visible in the user's working tree. A default output
   location for a secret-bearing file must be a private scratch (`mkdtemp`), never
   `os.getcwd()` / `.`.

2. **Masked on read.** A secret is never echoed back by a read path — API
   responses, the UI, logs, audit records, error messages. Secret-bearing fields
   are redacted (`***`) on serialization; the write path accepts them, the read
   path never returns them. (Free-form blobs like a connection `extra` are
   redacted by key name — `isSensitiveKey` — recursively.)

The two paths mirror each other: locality keeps a secret off disk where it could
be committed; masking keeps it out of responses where it could be observed.

## Consequences

Enforcement — every feature touching a secret carries this, and review checks it:

- **Materializing a credential to disk** → target the executor-injected scratch
  (the `DBT_PROFILES_DIR`/`DBT_TARGET_PATH`/`DBT_LOG_PATH` pattern: the pod base
  image and the Lite subprocess executor both provide one), and the code's own
  fallback, when the env is unset, is a private `mkdtemp` — **never the CWD**.
- **Serializing a secret to a response/log/audit** → mask secret-bearing fields
  (reuse `isSensitiveKey`); prefer omitting write-only fields entirely.
- **Tests are mandatory and specific**: a feature that writes a credential proves
  (unit + an inner-loop e2e) that nothing lands in the CWD/project; a feature that
  reads one proves the response is masked. UI-surfaced behavior additionally
  carries Playwright coverage against a real backend.
- **Symmetry Lite ↔ pod**: the two execution paths must provide the *same* secret
  locality. A fix that only lands in the pod base image (as ADR 0060's `/tmp`
  default did) but not in the Lite executor is incomplete — #882 was exactly that
  asymmetry.
- Optional CI guard: flag a secret-writing path that resolves its dir from
  `os.getcwd()` / `"."`.

The cost is small — a scratch dir and a masking helper — and it is paid once per
feature, against a class of leak that is severe (a committed or echoed live
credential) and, as the three issues show, easy to reintroduce by omission.

This ADR is a locality/read invariant, not a new mechanism; it does not change the
vault, the exchange, or scoping. It states the rule those already assume so the
next feature does not have to rediscover it through an incident.
