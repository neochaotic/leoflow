# Contributing to Leoflow

Thank you for your interest in contributing to Leoflow! This document explains how to get involved.

## Before You Start

1. Read [`README.md`](README.md) to understand what Leoflow is.
2. Read the [Architecture Decision Records](docs/adr/) under `docs/adr/`. These document non-negotiable design choices. Contributions that contradict an ADR will be rejected unless the ADR is first amended via a separate PR.
3. Read [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## How to Contribute

### Reporting Bugs

1. Search [existing issues](../../issues) to confirm the bug has not been reported.
2. If not, open a new issue using the **Bug Report** template.
3. Include reproduction steps, expected behavior, actual behavior, and environment details (OS, Go version, K8s version if applicable).

### Suggesting Features

1. Open an issue using the **Feature Request** template.
2. Describe the use case and the proposed solution.
3. **For significant features, propose an ADR.** Open a PR adding a draft ADR under `docs/adr/` with status "Proposed." Discussion happens on the PR.

### Submitting Code

We welcome pull requests. To make the review process fast and pleasant:

#### 1. Discuss First (for non-trivial changes)

Open an issue or comment on an existing one before starting work on anything beyond a small bug fix. This avoids duplicate effort and misaligned designs.

#### 2. Follow the Engineering Standards

Leoflow has strict engineering standards documented in the ADRs:

- **[ADR 0011 — TDD Strict](docs/adr/0011-tdd-strict.md):** every production change is preceded by a failing test. Two-commit pattern preferred (`test:` followed by `feat:`).
- **[ADR 0012 — Code Quality Standards](docs/adr/0012-code-quality-standards.md):** Go Report Card A+ as floor. GoDocs mandatory on every exported identifier. Cyclomatic complexity ≤ 15.
- **[ADR 0014 — Supply Chain Security](docs/adr/0014-supply-chain-security.md):** vulnerability scans, signed commits encouraged, no introduction of unsafe patterns.

CI enforces all of the above automatically. PRs that fail CI cannot be merged.

#### 3. Branch and PR Conventions

- Branch from `main`. Name: `feat/<short-description>`, `fix/<short-description>`, `docs/<short-description>`, `test/<short-description>`.
- One logical change per PR. If you find yourself describing the PR with "and also...", split it.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):
  - `feat: add XCom schema validation`
  - `fix: handle pod OOMKilled in K8s executor`
  - `test: failing test for retry backoff`
  - `docs: clarify executor configuration`
  - `chore: bump dependencies`
  - `refactor: extract scheduler decision logic`

#### 4. Pull Request Process

1. Fork the repository and create your branch.
2. Make your changes following TDD discipline.
3. Run `make lint test` locally before pushing.
4. Push and open a PR using the template.
5. Fill in the PR description completely. Linked issue, what changed, what was tested, screenshots if UI is affected.
6. Wait for CI. If any check fails, fix and push again.
7. Address review feedback. We aim to review within 3 business days.
8. Maintainers will squash-merge or rebase-merge based on the change.

## Security-Sensitive Changes

Changes to the following areas require extra review and are not accepted from first-time contributors:

- `internal/auth/` (JWT, RBAC)
- `internal/executor/` (pod creation, K8s API)
- `internal/storage/` (SQL queries, database access)
- `migrations/` (schema changes)
- `proto/` (gRPC contract between core and agent)
- Anything affecting how credentials or secrets are handled

If you have a contribution in these areas, please open a discussion issue first.

## Development Environment

```bash
# Clone the repo
git clone https://github.com/neochaotic/leoflow.git
cd leoflow
```

### See it run first (one command)

```bash
docker compose --profile demo up --build
# open http://localhost:8080 — log in as admin@leoflow.local / admin
# stop with: docker compose --profile demo down   (add -v to wipe data)
```

### Set up for development

```bash
# Optional, for Claude Code users (the file is gitignored):
cp docs/agent-templates/CLAUDE.md.template ./CLAUDE.md

make setup        # Go tools, Python parser/runtime, pre-commit hook
make build        # build bin/leoflow, bin/leoflow-server, bin/leoflow-agent
make dev-up       # start Postgres + Redis (Docker) and apply migrations
make lint test    # the quality gates you must pass before pushing
```

For an end-to-end author→run loop without Kubernetes, use `leoflow dev`
(see [`docs/operating-modes.md`](docs/operating-modes.md)).

## Project Layout

See [`README.md`](README.md) for the high-level layout. Detailed module documentation lives in package-level `doc.go` files (mandatory per ADR 0012).

## Communication

- GitHub Issues for bugs and feature requests
- GitHub Discussions for design questions and general help
- The OpenSSF Slack `#leoflow` channel for real-time discussion (link in README)

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).

## Recognition

All contributors are listed in [`CONTRIBUTORS.md`](CONTRIBUTORS.md) (updated periodically by maintainers). Significant contributions are also highlighted in release notes.

Thank you for helping make Leoflow better!
