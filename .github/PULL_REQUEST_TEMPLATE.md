<!-- Thanks for contributing! Keep PRs to one logical change. -->

## What & why

<!-- What does this change do, and why? -->

Closes #

## How it was tested

<!-- Commands you ran and the tests you added. Leoflow is strict TDD (ADR 0011):
     the test came first and failed before the implementation existed. -->

## Checklist

- [ ] **TDD** — a failing test preceded the production code (red → green → refactor) — [ADR 0011](https://github.com/neochaotic/leoflow/blob/main/docs/adr/0011-tdd-strict.md)
- [ ] `make lint test` passes locally
- [ ] GoDocs on every new exported identifier; cyclomatic complexity ≤ 15 — [ADR 0012](https://github.com/neochaotic/leoflow/blob/main/docs/adr/0012-code-quality-standards.md)
- [ ] No new dependency without justification; `make vuln` clean — [ADR 0014](https://github.com/neochaotic/leoflow/blob/main/docs/adr/0014-supply-chain-security.md)
- [ ] Public `/api/v2/` surface unchanged, or Airflow 3.2.x compatibility preserved
- [ ] Docs updated if behavior, flags, or config changed
- [ ] All code, comments, and commit messages are in English
- [ ] One logical change (no "and also…")

<!-- UI change? Add before/after screenshots.
     Touching internal/auth, internal/executor, internal/storage, migrations/, or
     proto/? Note it here — those areas get extra review (see CONTRIBUTING.md) and
     are not accepted from first-time contributors. -->
