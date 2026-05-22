# ADR 0018: UI Custom as Strategic North Star

**Status:** Accepted
**Date:** 2026-05-22
**Deciders:** Project founder

## Context

For the MVP, Leoflow serves the unmodified Apache Airflow 3.2.1 React SPA and
implements the internal `/ui/*` API the SPA depends on, pinned exactly to the
3.2.1 contract (see `docs/ui-compatibility.md`, ADR 0017). This buys a usable,
familiar UI quickly without writing one.

That choice carries a structural fragility we want recorded so it does not
silently calcify into a permanent decision:

- The Airflow `/ui/*` API is **internal and explicitly unstable** upstream
  (AIP-84). It is not a public contract; it can change shape between minor
  Airflow releases with no compatibility guarantee.
- The dominant failure mode is a **silent misrender**: a subtly wrong response
  shape produces a broken screen, not a clean error. The only reliable proof of
  correctness is a real browser walking the flows — automated schema checks and
  unit tests are necessary but not sufficient.
- Leoflow's internal domain model is richer than Airflow's vocabulary; pinning to
  Airflow's UI contract constrains what we can express and forces translation at
  the edge.

We accept this trade-off for MVP velocity. This ADR records that acceptance and
the long-term intent, so the team treats the pin as a deliberate, temporary
tactic rather than the destination.

## Decision

**Pinning the Airflow 3.2.1 UI is a tactical MVP decision. A purpose-built
Leoflow UI is the strategic north star.**

- Short term (MVP, Phase 5): ship the pinned Airflow 3.2.1 SPA + `/ui/*`
  implementation. Optimize for time-to-usable-UI.
- Long term: replace it with a custom Leoflow UI built directly on the stable
  public `/api/v2/` surface (and richer internal endpoints as needed), owned by
  Leoflow and free of the unstable `/ui/*` dependency.

No deadline is set. This ADR registers **intent and direction**, not a schedule.
The custom UI is not in any committed phase yet; it becomes a planning candidate
once the MVP demonstrates product-market signal.

## Rationale

- **Velocity now, optionality later.** The pin lets the MVP demo a complete
  orchestrator-with-UI in minutes; the north star keeps the door open to shed the
  fragile dependency without re-litigating the original decision.
- **Visibility prevents calcification.** Writing the long-term intent down means
  future contributors see the pin as a known liability with a planned exit, not
  as settled architecture.
- **Stable foundation already exists.** `/api/v2/` is the Airflow-compatible,
  publicly stable contract we already maintain; a custom UI built on it inherits
  that stability and avoids `/ui/*` entirely.

## Consequences

- Every `/ui/*` endpoint added during Phase 5 is understood to be **disposable**
  — work that the custom UI will eventually retire. We invest in it
  proportionally (pinned, documented, minimal), not as a long-lived contract.
- `docs/ui-compatibility.md` remains the living record of what the pin requires
  and what we learned; it doubles as the requirements input for the eventual
  custom UI.
- When the custom UI is built, the `/ui/*` surface and the embedded SPA
  (ADR 0017) can be removed; `/api/v2/` stays.
- Until then, "UI done" for any feature is only provable in a browser. Automated
  checks guard shape regressions but do not certify the screen renders.

## Alternatives Rejected

- **Build the custom UI now (skip the pin):** maximizes long-term cleanliness but
  blows the MVP timeline — a full Airflow-equivalent UI is months of work before
  the first usable demo. Rejected for the MVP; this is the north star, not the
  MVP path.
- **Treat the pin as permanent:** accepts the unstable `/ui/*` dependency
  indefinitely and lets it calcify. Rejected — the fragility is real and we want
  a recorded exit.
