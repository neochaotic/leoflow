---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0039-generated-connector-catalog.html
# --- end AUTO redirect aliases ---
title: "ADR 0039: Generated connector catalog with full form fidelity"
linkTitle: 0039 · Generated connector catalog with full form fidelity
weight: 390
description: "ADR 0039: Generated connector catalog with full form fidelity"
---

**Status:** Accepted
**Date:** 2026-06-04
**Supersedes (in part):** ADR 0036 §"Form widgets" — the decision to return `extra_fields: {}` and accept a generic connection form.
**Companions:** ADR 0038 (`connectors:` sugar), ADR 0024 (parser structural shim), ADR 0014 (admin panel is Go-only)

## Context

The admin connection form is driven entirely by `GET /ui/connections/hook_meta`.
Until now Leoflow served a **hand-written** registry of ~15 connector types
(`internal/connectors`) that carried only the standard-field *hidden* flags and an
empty `extra_fields: {}` (ADR 0036 named this tradeoff: generic form, runtime
unaffected). Two problems surfaced when we looked closely:

1. **Coverage.** 15 curated types out of Airflow's ~90 providers. The long tail
   worked only via the `dependencies:` escape hatch, with no dropdown entry.
2. **Field fidelity.** With `extra_fields: {}`, the form rendered only
   host/login/password/port/schema + a raw "Extra JSON" textarea. For cloud /
   config connectors (Snowflake `account`/`warehouse`, AWS keys, GCP keyfile,
   Kafka config) the actual credential fields are **provider-specific**, so the
   user had to hand-type JSON. We also dropped the standard-field **relabels**
   (Postgres "Schema" → "Database") and placeholders.

We verified (bundle archaeology + Airflow docs + issues #60370/#21188) that the
embedded Airflow 3.2 SPA **does** render `extra_fields` via its `FlexibleForm`
component. So the empty `{}` was leaving real UX on the table.

## Decision

**Generate the connector catalog from a real Airflow install instead of hand-
maintaining it, and serve full form fidelity (`standard_fields` + `extra_fields`).**

- `scripts/gen_connectors.py` runs offline in an env with `apache-airflow` + the
  target providers (`scripts/connectors-providers.txt`). It calls Airflow's **own**
  serializer — `HookMetaService.hook_meta_data()`, the exact code path behind
  `/ui/connections/hook_meta` — plus `ProvidersManager` for the pip package per
  conn_type, and writes `internal/connectors/catalog.json`.
- The Go `connectors` package embeds that JSON (`//go:embed`) and is the single
  source of truth for all three consumers: the admin form, the `connectors:`
  sugar, and compile-time validation. A small hand-curated overlay adds sugar
  aliases (`gcp`/`google`) that Airflow's vocabulary lacks.
- The form handler serves each entry's `standard_fields` + `extra_fields`
  verbatim — the precise FlexibleForm shape — so the SPA renders the labeled,
  provider-specific fields.

### Why generated, not hand-written

Hand-maintaining ~90 providers × N fields each is exactly the whack-a-mole
maintenance debt that got the runtime shim deferred (ADR 0036). Generating from
Airflow's own serializer means we never translate WTForms widgets by hand and
never drift: bumping a provider version + re-running the script regenerates the
truth. `catalog.json` is a committed, reviewable artifact (~100 KB).

## Consequences

- **~86 connector types** now ship with a dropdown entry, the correct
  standard-field behavior (hide/relabel/placeholder), and provider-specific
  `extra_fields`. The long tail still also works via `dependencies:`.
- **Regeneration is a documented, reproducible step**, not editing. `catalog.json`
  must never be edited by hand; the provider list is pinned in
  `scripts/connectors-providers.txt`.
- **Known upstream caveat:** Airflow 3.2 FlexibleForm marks some sensitive fields
  as required incorrectly (#60370). We inherit Airflow's serialized schema as-is;
  if it bites, the fix belongs upstream (we do not fork the metadata).
- **Core conn types** (`generic`, `email`) carry no pip package: they appear in
  the form but are not resolvable by the `connectors:` sugar (nothing to install).
- ADR 0036's runtime-shim deferral is unchanged; this ADR only supersedes its
  form-widget `{}` decision.
