# ADR 0037: Release version scheme — skip alpha/beta, RC discipline from `v0.0.1`

**Status:** Accepted
**Date:** 2026-06-03
**Companions:** ADR 0028 (one tag = both editions), ADR 0033 (RC tags + E2E gates)
**Supersedes (partial):** clause of ADR 0033 stating *"Effective from `v0.1.0-alpha.1`"*

## Context

Leoflow has been shipping `v0.0.1-prealpha.N` since the project started. The next
question — and one the existing release ADRs left open — is how the version
scheme evolves after the pre-alpha series ends.

Two industry-standard answers exist:

1. **Alpha → Beta → RC → Stable** (the classic phase ladder). Each phase
   communicates a maturity bracket: alpha = experimental, beta = API freeze,
   RC = feature freeze, stable = supported. Promoted by Java/Eclipse-era
   projects; baked into ADR 0033 as the assumed shape.
2. **RC-only progression on every release**. No alpha/beta phases. Every tag
   that ships is either an RC waiting on E2E gates or a stable tag once gates
   pass. The `0.x.y` SemVer prefix already signals "pre-1.0, breaking changes
   allowed"; the maturity word is redundant. Used by Astral (uv, ruff), many
   modern Rust/Go projects, and (in spirit) Postgres core.

The phase ladder is heavier than Leoflow needs at this size. Defining "when
alpha becomes beta" is subjective; in practice every project that adopts it
spends meeting time on the question. SemVer's own `0.x.y` namespace already
communicates "experimental — breaking changes possible"; restating that with
the word `-alpha.N` adds rituals without information. The RC gate is the only
mechanism that produces an objective promotion signal — and we already have it
(ADR 0033's E2E gates), so we can rely on it for every release rather than
reserving it for the final mile.

The decision below codifies the RC-only path and renumbers ADR 0033's
effective-from threshold accordingly.

## Decision

1. **The pre-alpha series ends with a single `v0.0.1` tag.** Not
   `v0.1.0-alpha.1`, not `v0.0.1-alpha.1` — the plain `v0.0.1`. That tag
   marks the moment the project stops calling itself "pre-alpha" and starts
   honouring the discipline below. Pre-alpha cuts before it continue under
   the existing `v0.0.1-prealpha.N` convention until the operator (the
   project owner) decides the next non-prealpha cut is `v0.0.1`.

2. **Every release after `v0.0.1` follows the RC discipline.** A new
   release goes through `vX.Y.Z-rc.N` → `vX.Y.Z` mediated by the E2E gates
   from ADR 0033. There are no `alpha`/`beta` suffixes; the only
   pre-release suffix this project uses going forward is `-rc.N`.

3. **SemVer carries the maturity signal, not a word in the tag.**
   - `v0.x.y` means "pre-1.0; breaking changes are allowed between any two
     releases, with clear release notes." This is the standard SemVer
     contract.
   - `v1.0.0` means "API contract — breaking changes only in a major
     bump." When that's cut requires an explicit decision; this ADR does
     not schedule it.

4. **Minor (`vX.Y+1.0`) vs patch (`vX.Y.Z+1`) policy in the `0.x`
   range.**
   - **Patch (`vX.Y.Z+1`):** bug fixes, security patches, polish, small
     features that do not change the public surface in a breaking way.
     Default for most releases in `0.x`.
   - **Minor (`vX.Y+1.0`):** introduces a substantial new feature or a
     deliberately breaking change. Examples on the current roadmap: ADR
     0036's runtime compatibility shim landing → `v0.1.0`; an AWS
     connector tier 80/20 cut → `v0.2.0`.
   - Even in `0.x`, every breaking change ships in release notes with a
     migration guide.

5. **No alpha or beta tags going forward.** If someone wants to test a
   pre-release artifact, they pick an `-rc.N` tag from the GitHub
   releases page (or the install script's `LEOFLOW_VERSION=…` flag).
   `-rc.N` artifacts are marked as pre-releases on GitHub so package
   managers and `latest` queries skip them by default.

6. **ADR 0033 amendment.** ADR 0033 declares its RC-tags + E2E-gates flow
   *"effective from `v0.1.0-alpha.1`"*. That clause is **superseded**:
   the flow is **effective from `v0.0.2-rc.1`** (the first RC after
   `v0.0.1`). `v0.0.1` itself uses the same simpler direct-tag flow as
   the pre-alpha series — one final exception so the alpha cut does not
   block on the RC plumbing being in place.

7. **README and install docs carry a `🧪 Experimental — pre-1.0` notice
   for as long as the version starts with `0.`.** Removed only when
   `v1.0.0` ships. This is the user-facing maturity signal that
   replaces the missing `-alpha`/`-beta` words.

8. **Cadence is event-based, not time-based.** A release happens when a
   set of fixes or features is ready and the RC's E2E gates pass. There
   is no fixed weekly/monthly cadence in the `0.x` line. (Once `v1.0.0`
   ships a cadence ADR can revisit this.)

## Consequences

- **Simpler narrative.** Users only ever see `vX.Y.Z` or `vX.Y.Z-rc.N`.
  Marketing copy and changelogs don't have to spend paragraphs explaining
  "what phase are we in." The version number alone says it.
- **Objective promotion signal.** RC → stable is gated by E2E (ADR 0033),
  not by subjective "is the API frozen?" debate.
- **Recovery is fast.** A critical bug in `v0.0.1` ships as `v0.0.2-rc.1`
  the same day; the E2E gate runs; promotion to `v0.0.2` happens when
  green. No "we need to call this a beta first" detour.
- **The minor/patch line stays meaningful inside `0.x`.** Because every
  release goes through an RC, the version number alone doesn't tell the
  user "is this safe to upgrade?" — but the release notes do, and the RC
  gate ensures no obvious regression slips through.
- **`v0.0.1` becomes the project's actual public debut.** The discipline
  it codifies (`make ci-local` clean, README/Helm docs aligned, install
  script tested across distros, the install-smoke gate green) is the
  promotion bar. Pre-alpha tags before it are dev iteration; `v0.0.1` is
  the first tag the project recommends to a stranger.
- **No backwards-incompatibility with ADR 0028.** ADR 0028's
  "one tag = both editions" stays the underlying rule. ADR 0037 just
  governs what those tags look like.

## Alternatives considered

- **Stay with the alpha/beta/RC ladder (ADR 0033's original assumption).**
  Rejected: adds vocabulary without information when SemVer already
  encodes maturity. The phase-transition decisions are subjective and
  consume meeting time. For a project of Leoflow's current size, the cost
  outweighs the benefit.
- **CalVer (`YYYY.MM.PATCH`).** Rejected: harder to communicate breaking
  changes (the version number has no signal for that); cadence-implicit
  in the format (we want event-based for now). Worth revisiting at
  `v1.0` if we cement a monthly cadence.
- **Skip the RC suffix too — straight `vX.Y.Z` from CI.** Rejected: the
  E2E gate from ADR 0033 needs a separate identifier for the candidate
  build vs the promoted build (we don't republish artifacts under a new
  tag once gates pass — the same tag *is* the candidate, gated). Keeping
  `-rc.N` preserves that.
- **Defer the question until after `v0.0.1`.** Rejected: the operator
  cutting `v0.0.1` needs the answer to know what comes next. Locking it
  now removes a decision the operator would otherwise make under
  release-day pressure.
