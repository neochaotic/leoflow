# Phase 1 Prompt — Foundation

## Goal

Establish the foundation of the Leoflow project: monorepo skeleton, build tooling, database schema, the DAG/leoflow schemas, and the basic CLI.

By the end of this phase, a developer can:

1. Clone the repo, run `make setup`, and have a working dev environment.
2. Run `leoflow init` to scaffold a new DAG project (`leoflow.yaml` + `dag.py`).
3. Run `leoflow validate` to check the `leoflow.yaml` and `dag.py` for errors.
4. Run `leoflow compile` to produce a `dag.json` (build/push of the image can be stubbed for this phase).
5. Run `make migrate-up` to create all tables in a local Postgres.

## Constraints

- Read `CLAUDE.md` and all ADRs in `docs/adr/` before starting. **ADRs 0011 (TDD), 0012 (Code Quality A+), and 0014 (Supply Chain) govern every change in this phase.**
- All code, comments, identifiers, and commit messages in **English only**.
- Stack: Go 1.22+, Cobra, Viper, slog, sqlc, golang-migrate, testify.
- Python parser: 3.11, uses Apache Airflow SDK 3.2.x.
- Apache 2.0 license header on every Go file.
- **Go Report Card A+ from the first commit.** All seven checks pass at 100% locally and in CI before any code is merged.
- **GoDoc on every exported identifier.** Functions, types, methods, constants, and variables that start with an uppercase letter MUST have a doc comment that starts with the identifier name and ends with a period. The linter blocks merge otherwise.
- **Cyclomatic complexity ≤ 15** per function. Refactor before merging if a function exceeds the threshold.
- **`make lint` must pass** before any commit. The pre-commit hook enforces this locally; CI re-checks.

## TDD Workflow (Non-Negotiable)

For every behavior added in this phase, the workflow is:

1. **Write a failing test.** Place it in the appropriate `_test.go` file.
2. **Run the test.** Confirm it fails for the right reason (assertion failure, not compile error or missing import).
3. **Commit the failing test** with message `test: failing test for <behavior>`.
4. **Implement the minimum code** to make the test pass.
5. **Run the test.** Confirm green.
6. **Refactor** if needed. Re-run all tests. Confirm green.
7. **Commit the implementation** with message `feat: <behavior>`.

Two-commit pattern (test commit + implementation commit) is **strongly preferred**. Combined commit is acceptable only if the test was clearly written first within the editing session.

Forbidden patterns:

- Writing production code "to see how it should look" before any test exists.
- Generating test files at the end "to satisfy coverage."
- Skipping the red phase ("I'll just write both and run them together").
- Claiming tests pass without showing the command output.

Coverage floor for Phase 1: **70% per package** (excluding `cmd/*/main.go` and generated code). CI rejects below floor.

## Quality Workflow (Non-Negotiable, ADR 0012)

In parallel with TDD, every change must satisfy Go Report Card A+ rules from the first commit:

1. **Before opening the editor**, ensure `make setup` has installed the pre-commit hook. It runs `gofmt`, `go vet`, `golangci-lint run`, and `go test -short` automatically.
2. **When adding any exported identifier**, write its GoDoc comment in the same edit. The comment starts with the identifier's name and ends with a period:
   ```go
   // ParseDAGFile reads a Python DAG file and returns its serialized representation.
   // It returns ErrInvalidSyntax when the file cannot be parsed as a Leoflow DAG.
   func ParseDAGFile(path string) (*DAGSpec, error) { ... }
   ```
3. **Watch the complexity.** If any function approaches 15 cyclomatic branches, extract a helper before submitting.
4. **Run `make lint` before every commit.** Output must be clean. If a linter complains, fix it — do not disable the linter.

Forbidden patterns:

- Adding an exported function without its GoDoc.
- Disabling a linter to make a commit pass (`//nolint:...` is allowed only with a rationale comment and approval).
- Pushing code that fails `goreportcard-cli` locally.
- Long functions kept "for clarity" when they exceed complexity 15.

## Supply Chain Hygiene (ADR 0014)

Phase 1 wires in the security baseline that subsequent phases inherit:

- `govulncheck` runs in CI on every PR (`.github/workflows/security.yaml` is provided; verify it works).
- Dependabot is enabled (`.github/dependabot.yaml` is provided).
- All Go module dependencies pinned to specific versions in `go.mod`.
- All GitHub Actions in workflows pinned by SHA, not tag.

## Deliverables

### 1. Repository scaffolding

```
leoflow/
├── go.mod
├── go.sum
├── Makefile
├── .gitignore
├── .editorconfig
├── .golangci.yaml
├── LICENSE                                # Apache 2.0
├── CLAUDE.md                              # already exists
├── README.md                              # already exists
├── docs/                                  # already exists
├── migrations/                            # already exists
├── proto/                                 # already exists
├── cmd/leoflow/main.go                    # CLI entry point
├── internal/
│   ├── cli/                               # Cobra commands
│   ├── config/                            # Viper-based config
│   ├── domain/                            # core types
│   └── version/                           # build version, embed at link time
└── parser/
    ├── pyproject.toml
    ├── leoflow_parser/
    │   ├── __init__.py
    │   ├── cli.py
    │   └── compiler.py
    └── tests/
```

### 2. Makefile targets

- `make setup` — install Go deps, install pre-commit, install Python parser
- `make build` — build all binaries to `./bin/`
- `make test` — run Go and Python tests
- `make lint` — golangci-lint + ruff
- `make migrate-up` / `make migrate-down`
- `make sqlc` — regenerate sqlc code
- `make proto` — regenerate protobuf code (Phase 3, leave a stub here)
- `make clean`

### 3. CLI commands (Cobra)

Implement these in `internal/cli/`:

- `leoflow version` — print version, git commit, build date
- `leoflow init` — scaffold a new DAG project with sensible defaults
- `leoflow validate [path]` — validate `leoflow.yaml` and dag.py against schemas
- `leoflow compile [path] --output dag.json` — invoke the Python parser, produce `dag.json`
- `leoflow push <dag.json>` — stub for Phase 2 (print "not yet implemented")
- `leoflow auth create-token` — stub
- `leoflow server` — stub for Phase 2

Configuration loading via Viper:
- File: `~/.leoflow/config.yaml`
- Env vars: `LEOFLOW_*` prefix
- Flags override env vars override file

### 4. Domain types

In `internal/domain/`, define Go structs that mirror the JSON schemas:

- `DAGSpec` — matches `docs/api/dag-schema.json`
- `LeoflowConfig` — matches `docs/api/leoflow-yaml-schema.json`
- `TaskSpec` (nested in `DAGSpec`)
- Validation methods: `Validate() error` on each, returning `multierr` of issues

Use struct tags for JSON and YAML marshaling. Use `github.com/santhosh-tekuri/jsonschema/v5` to validate against the JSON Schema files embedded via `//go:embed`.

### 5. Python parser

The parser is a small Python package that:

1. Imports a `dag.py` file using importlib.
2. Uses the Airflow SDK to detect `with DAG(...) as dag:` blocks and `@task` decorators.
3. Walks the dependency graph.
4. Emits a `dag.json` matching `dag-schema.json`.

The parser is invoked by `leoflow compile` as a subprocess. The interface:

```
python -m leoflow_parser compile \
    --source ./dag.py \
    --config ./leoflow.yaml \
    --output ./dag.json \
    --image <image_reference>
```

For Phase 1, the parser supports:
- `PythonOperator` (entrypoint inferred from callable's `__module__` and `__name__`)
- `BashOperator` (`bash_command` becomes the entrypoint)
- `HttpOperator` (`endpoint`, `method`, `headers`, `data` become `http_request`)
- Task dependencies via `>>`, `<<`, `set_upstream`, `set_downstream`
- Trigger rules from `trigger_rule` parameter

Out of scope for Phase 1:
- Dynamic task mapping (`expand`, `expand_kwargs`)
- TaskGroups (treat as flat for now, with a `TODO` comment)

### 6. Database migrations

The migrations already exist in `migrations/`. Wire them into `make migrate-up`/`make migrate-down` using `golang-migrate/migrate`.

Provide a `docker-compose.dev.yaml` with Postgres 16 and Redis 7 so developers can run `docker compose -f docker-compose.dev.yaml up -d` and then `make migrate-up`.

### 7. Tests

- Unit tests for each domain validation function.
- A golden-file test for the CLI: feed it a `dag.py` and assert the produced `dag.json` matches a checked-in expected file.
- Pytest suite for the parser with 3 fixture DAGs (simple linear, branching, mixed operators).

### 8. CI workflow stub

A `.github/workflows/ci.yaml` running on push:

- Go: `go test ./...`, `golangci-lint run`
- Python: `pytest`, `ruff check`
- Schema validation: ensure embedded JSON Schemas are valid

## Acceptance Criteria

- `make build` produces `bin/leoflow`.
- `leoflow init my-dag` creates a folder with valid `leoflow.yaml` and `dag.py`.
- `leoflow validate my-dag/` returns 0 on the scaffold.
- `leoflow compile my-dag/ --output dag.json --image test:v1` produces a `dag.json` that validates against `dag-schema.json`.
- `make migrate-up` succeeds against the dev Postgres.
- `make test` passes with at least **70% per-package coverage** (CI floor).
- **Commit history shows the TDD pattern.** Spot-checking 5 random commits should reveal a `test:` commit immediately preceding the matching `feat:` commit, or a single combined commit where the test is clearly authored first.
- Pre-commit hook installed and verified blocking commits that decrease coverage by more than 1%.
- **`make lint` exits 0 with zero warnings.** All golangci-lint linters listed in `.golangci.yaml` pass.
- **`goreportcard-cli .` reports grade A+ (≥99% per check).** Includes 100% on gofmt, go_vet, gocyclo, golint, ineffassign, misspell, license.
- **Every exported identifier has a GoDoc comment** starting with the identifier name and ending with a period. `revive` enforces this; spot-check a random sample of 10 exported symbols manually.
- **`govulncheck ./...` reports zero affecting vulnerabilities.**

## Hints

- For `//go:embed` of JSON Schemas, place them under `internal/domain/schemas/` and copy at build time, or just use the absolute path `docs/api/`.
- For the Python parser, do NOT execute the user's task functions; only inspect the DAG structure. Use `dag.task_dict` from the Airflow SDK.
- The CLI should print friendly error messages, not Go panics. Wrap unexpected errors and exit with code 1.

## Out of Scope (Do Not Implement in Phase 1)

- HTTP API server (Phase 2)
- Scheduler logic (Phase 2)
- Executor (Phase 3)
- gRPC agent (Phase 3)
- XCom storage (Phase 4)
- Log shipping (Phase 4)
