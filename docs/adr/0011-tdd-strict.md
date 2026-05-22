# ADR 0011: Test-Driven Development (Strict)

**Status:** Accepted
**Date:** 2026-05-21

## Context

Leoflow has a high bar for correctness: a workflow orchestrator that loses state, double-schedules tasks, or silently drops data is worse than no orchestrator. The codebase will be developed substantially with AI coding agents, which produce code fast but can produce plausible-looking code that is subtly wrong.

Two failure modes must be prevented:

1. **Code that compiles but is incorrect.** Type-safe Go catches some bugs; logic bugs need tests.
2. **Code where the author "knew it worked" but never wrote the test.** Six months later, a refactor breaks behavior nobody documented.

A disciplined TDD workflow addresses both, *if* it is genuinely enforced rather than retrofitted.

## Decision

Leoflow follows **strict Test-Driven Development**. Every line of production code is written in response to a failing test. The cycle is:

1. **Red.** Write a test that expresses the desired behavior. Run it. Confirm it fails for the right reason (assertion failure, not compile error).
2. **Green.** Write the minimum production code that makes the test pass. Nothing more.
3. **Refactor.** Improve structure with the test suite as the safety net. All tests must remain green.

This is enforced at three levels:

### Level 1 — Tooling

- A pre-commit hook runs `go test ./...` and blocks the commit on any failure.
- A pre-commit hook runs `go test -cover` and blocks commits that decrease coverage by more than 1%.
- CI rejects any pull request where new production code (`.go` files outside `_test.go`) was added without a corresponding new or modified test in the same commit.

### Level 2 — Review

- Pull requests must have a description listing the tests added. "Refactored X" is acceptable only if no behavior changed and existing tests cover it.
- Pull requests that add a new public function without a corresponding `Test*` are rejected automatically by a CI rule, before human review.

### Level 3 — AI agent discipline

When working with Claude Code or similar AI agents, the workflow is explicit and non-negotiable:

```
1. State the behavior to add.
2. Ask the agent to write the failing test first. Run it. Confirm it fails.
3. Ask the agent to implement. Run the test. Confirm it passes.
4. Ask the agent to refactor if needed. Run all tests.
5. Commit.
```

Skipping step 2 (writing the test first) is the most common shortcut. The discipline is to never accept "I'll add tests at the end" — the test always comes first.

## What Counts as a Test

| Type | Counts? | When to use |
|---|---|---|
| Unit test (`_test.go` with `t *testing.T`) | ✅ | Default for all logic |
| Table-driven test | ✅ | Multiple inputs/outputs for the same function |
| Integration test (`//go:build integration`) | ✅ | Behavior across process boundaries (DB, Redis, gRPC) |
| Property-based test (`testing/quick` or `rapid`) | ✅ | State machines, parsers, serializers |
| End-to-end test (Phase 5+) | ✅ | UI flows |
| "Run it manually and look" | ❌ | Never counts as a test |
| Commented-out `TODO: add test` | ❌ | Treated as missing test |

## Coverage Targets (Minimum, Not Goal)

| Phase | Minimum coverage |
|---|---|
| Phase 1 | 70% |
| Phase 2 | 75% |
| Phase 3 | 75% |
| Phase 4 | 80% |
| Phase 5 | 80% |
| Phase 6 | 85% |

Coverage is computed per-package. Packages below threshold block CI. Excluded from coverage: `cmd/*/main.go`, generated code (sqlc, protobuf), `internal/version`.

These are **floors**, not targets. The actual goal is "every behavior that matters has a test that would catch its regression."

## What TDD Does NOT Mean

To prevent ritual misinterpretation:

- **Not** every getter has a test. Trivial code (struct field access, simple delegation) doesn't need one.
- **Not** every test must be unit-level. Integration tests are first-class.
- **Not** mocking everything. Prefer real implementations (testcontainers for Postgres/Redis) when the cost is low.
- **Not** 100% coverage. Beyond 85% the cost climbs faster than the value.

## State Machine Tests Are Mandatory

The scheduler's state machine (task instances transitioning through `none → scheduled → queued → running → success/failed/skipped/upstream_failed`) is the most safety-critical code in the project. Every legal transition and every illegal transition must have a test. This is non-negotiable.

A dedicated test suite `internal/scheduler/state_machine_test.go` exhaustively enumerates:

- For each `from_state × to_state` pair, assert allow/reject.
- For each trigger rule × upstream state combination, assert the resulting decision.
- For race conditions (e.g., two replicas attempting the same transition), assert serialization.

## Consequences

- Development is slower per feature, faster across the whole project. Fewer regressions, less debugging time.
- The test suite becomes a living specification. New contributors read tests to understand intent.
- Refactoring is cheap because the tests are dense.
- AI agents are forced into a verifiable loop: every change is observable as test transitions (red to green), not just claims of "this should work."

## Consequences for AI Agents Specifically

Agents that violate TDD discipline (write production code before tests, skip the red phase, or claim tests pass without running them) must be corrected immediately. Repeated violations in a session are grounds for restarting the session with explicit reminders.

The prompts under `prompts/phase-*.md` include TDD as an explicit constraint. Reviewers verify that the commit history shows the pattern: `test: failing test for X` followed by `feat: implement X`.

## Alternatives Rejected

- **"Tests after, when convenient":** rejected. This is what produces untested codebases. The window of "convenient" never opens.
- **Coverage gates without TDD:** rejected. Coverage can be achieved by tests that pass against any implementation; only TDD ensures the test failed against the *wrong* implementation first.
- **TDD only for critical paths:** rejected as too subjective. Every contributor would define "critical" differently.
