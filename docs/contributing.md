# Contributing

Contributions are welcome. The canonical guide is
[`CONTRIBUTING.md`](https://github.com/neochaotic/leoflow/blob/main/CONTRIBUTING.md);
this is the short version.

## Get set up
```bash
make setup          # Go tools, Python parser/runtime, pre-commit hook
make dev-install    # leoflow + server + agent + migrate on PATH
leoflow dev setup   # provision the dev environment
```

## The quality bar (non-negotiable)
- **Strict TDD** — every line of production code is written against a failing test
  (red → green → refactor). See ADR 0011.
- **Go Report Card A+** — gofmt, govet, gocyclo ≤ 15, golint with GoDocs on every
  export, ineffassign, misspell, license. `make lint` clean before every commit.
- **GoDocs on every exported identifier.** Tests + lint run in the pre-commit hook
  and CI; coverage may not drop > 1%.
- **English** for all code, comments, commits, docs.

## Workflow
1. Branch, write the failing test, implement, `make lint && go test ./...`.
2. For a new area, read the relevant [ADRs](adrs.md) first — they explain the *why*.
3. Open a PR; the gates (tests, lint, govulncheck/gosec/Trivy/CodeQL) must be green.

## Docs
This site is MkDocs Material; `mkdocs serve` to preview. The CLI/Go references are
auto-generated (Cobra / gomarkdoc) — don't hand-edit `docs/cli` or `docs/go`.
