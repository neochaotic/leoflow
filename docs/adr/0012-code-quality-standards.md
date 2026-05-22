# ADR 0012: Code Quality Standards (Go Report Card A+ as Floor)

**Status:** Accepted
**Date:** 2026-05-21

## Context

Code quality is easier to maintain when it is enforced from the first commit than to retrofit later. Go has a strong culture of canonical formatting and idiomatic style, codified by tools like `gofmt`, `go vet`, and `golint`. The Go Report Card aggregates seven of these checks into a single grade (A+ being the top).

Treating the A+ grade as a vanity badge would be teatro. Treating the underlying checks as **mandatory engineering hygiene** from day one is real.

## Decision

The Leoflow codebase MUST maintain a Go Report Card A+ grade (≥99% per check) at all times. This is enforced via CI gates that block merges, not as an aspirational goal.

The seven required checks are:

| Check | Threshold | What it enforces |
|---|---|---|
| `gofmt` | 100% | Canonical Go formatting |
| `go vet` | 100% | No suspicious constructs (printf format mismatches, locks copied, unreachable code) |
| `gocyclo` | 100% | No function with cyclomatic complexity > 15 |
| `golint` | 100% | Exported identifiers have GoDoc; idiomatic naming |
| `ineffassign` | 100% | No ineffectual assignments |
| `misspell` | 100% | No spelling errors (US English) |
| `license` | 100% | LICENSE file present at repo root |

Beyond the seven, an expanded `golangci-lint` configuration adds security and correctness checks (see Configuration below).

## GoDocs Are Mandatory

Every exported identifier — function, method, type, variable, constant — MUST have a GoDoc comment. The comment must:

1. Start with the identifier name. Example: `// Schedule examines runnable tasks and dispatches them.`
2. Be a complete sentence ending in a period.
3. Explain the *what* and the *why*, not just restate the signature.
4. Be written in English, no exceptions (consistent with ADR 0008 of agent context).

Examples:

```go
// Good
// Authenticate validates the supplied JWT token and loads the associated
// User and tenant into the returned context. It returns ErrInvalidToken
// if the token is malformed, expired, or signed by an unknown key.
func Authenticate(ctx context.Context, token string) (context.Context, error)

// Bad (just restates the signature)
// Authenticate authenticates a token.
func Authenticate(ctx context.Context, token string) (context.Context, error)

// Bad (missing the identifier prefix)
// Validates a token and returns a context with user info.
func Authenticate(ctx context.Context, token string) (context.Context, error)
```

Package-level GoDocs (`// Package foo ...`) are mandatory on every package, placed in a `doc.go` file when the package spans multiple files.

## Cyclomatic Complexity Cap

Cyclomatic complexity > 15 in any function is a CI failure. This forces the author to split logic into smaller named functions, which improves both readability and testability. Critical paths (scheduler decision logic, state machine transitions) target ≤ 10.

This pairs naturally with TDD (ADR 0011): small functions are easier to test exhaustively.

## Misspell

Misspell catches typos in identifiers, comments, and strings. US English is the canonical dictionary. The Go community has converged on US English in standard library documentation, so this matches the ecosystem.

## Configuration

The single source of truth for linting is `.golangci.yaml` at the repo root. It enables the seven A+ checks plus:

- `errcheck` — every error returned must be handled (no `_ = foo()`)
- `staticcheck` — comprehensive static analysis (replaces `golint` legacy)
- `unused` — flag unused identifiers
- `gosec` — security-oriented linting (catches SQL injection patterns, weak crypto, hardcoded creds; see ADR 0014)
- `revive` — modern golint replacement
- `gocritic` — opinionated style and performance suggestions

Configuration excludes generated code (sqlc output, protobuf-generated, `internal/version`).

## Enforcement

| Where | What |
|---|---|
| Local: pre-commit hook | Runs `gofmt -l`, `go vet`, `golangci-lint run` on staged files. Blocks commit on failure. |
| CI: every PR | Runs full `golangci-lint run` plus `goreportcard-cli`. Blocks merge on any check below 99%. |
| Periodic | Weekly job verifies the public Go Report Card endpoint still reports A+. Opens an issue automatically if degraded. |

## Why "A+ as a floor, not a target"

The seven checks set a minimum hygiene bar. They do NOT catch:

- Logic bugs (TDD catches these — ADR 0011)
- Architectural drift (ADRs catch these)
- Security vulnerabilities (govulncheck and gosec catch these — ADR 0014)
- Performance regressions (load tests catch these — Phase 6)

The grade is the floor below which we never sink. Real quality is engineered on top of it.

## Consequences

- Developers and AI agents are trained from day one to write small, well-commented functions.
- Cyclomatic complexity caps prevent the "300-line function with seven nested ifs" failure mode.
- GoDocs become the documentation that `godoc.org` / `pkg.go.dev` renders automatically. Operators and contributors benefit immediately.
- The badge in the README is a real signal of discipline, not a sticker.
- AI agents are constrained: they cannot generate exported functions without GoDoc; the CI will reject the PR.

## What This Is NOT

- This ADR does not mandate inline comments on every line of code. Comments inside function bodies are reserved for explaining *why* something non-obvious is done. Self-documenting code (good names, small functions) is the default.
- This ADR does not mandate GoDocs on unexported identifiers. They are encouraged but not required.
- This ADR does not impose subjective style preferences (e.g., where to place blank lines). Whatever `gofmt` outputs is correct.

## Alternatives Rejected

- **"Style guide as a PDF, no enforcement":** rejected because human review is inconsistent.
- **"A grade instead of A+":** rejected because the bar between A and A+ is the difference between "mostly clean" and "no exceptions."
- **"Custom Go linter":** rejected because golangci-lint is the community standard and bundles everything we need.
