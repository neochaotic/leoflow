# Leoflow — Master Implementation Guide

This document is the **starting point** for building Leoflow. It tells you which file to use, in which order, and how to drive Claude Code effectively.

## What You Have

Inside this `leoflow/` directory:

```
leoflow/
├── .gitignore                   # Excludes AI agent context files (CLAUDE.md, etc.)
├── .golangci.yaml               # Lint config enforcing Go Report Card A+ (ADR 0012)
├── README.md                    # Public-facing project description.
├── IMPLEMENTATION_GUIDE.md      # ★ This file.
├── SECURITY.md                  # Vulnerability disclosure policy (ADR 0014).
├── CONTRIBUTING.md              # Contribution guidelines.
├── .github/
│   ├── dependabot.yaml          # Automated dependency updates (ADR 0014).
│   └── workflows/
│       ├── ci.yaml              # Build, test, lint, A+ check.
│       ├── security.yaml        # govulncheck + gosec + Trivy + CodeQL.
│       ├── scorecard.yaml       # Weekly OpenSSF Scorecard.
│       └── release.yaml         # Signed releases (cosign).
├── docs/
│   ├── adr/                     # 14 Architecture Decision Records. Read in order.
│   │   ├── 0001-why-leoflow.md
│   │   ├── 0002-pod-per-task.md
│   │   ├── 0003-dag-as-image.md
│   │   ├── 0004-thin-agent.md
│   │   ├── 0005-hybrid-dag-authoring.md
│   │   ├── 0006-xcom-redis.md
│   │   ├── 0007-airflow-ui-compatibility.md
│   │   ├── 0008-jwt-auth.md
│   │   ├── 0009-leader-election.md
│   │   ├── 0010-observability.md
│   │   ├── 0011-tdd-strict.md              # TDD discipline
│   │   ├── 0012-code-quality-standards.md  # ★ Go Report Card A+ as floor
│   │   ├── 0013-scalar-api-docs.md         # ★ Scalar embedded in server
│   │   └── 0014-supply-chain-security.md   # ★ govulncheck + Trivy + Scorecard + badge
│   ├── agent-templates/
│   │   ├── README.md
│   │   └── CLAUDE.md.template              # Copy this to ./CLAUDE.md after cloning
│   └── api/
│       ├── dag-schema.json                 # JSON Schema for dag.json
│       ├── leoflow-yaml-schema.json        # JSON Schema for leoflow.yaml
│       └── openapi.yaml                    # Public REST API (Airflow-compatible subset)
├── migrations/                             # Postgres migrations 001-005
├── proto/agent.proto                       # gRPC contract for Agent ↔ Control Plane
└── prompts/                                # Phase-by-phase prompts for Claude Code
    ├── phase-1-foundation.md
    ├── phase-2-control-plane.md
    ├── phase-3-executor-agent.md
    └── phases-4-5-6.md
```

## ⚠️ Important: Agent Context Files Are Gitignored

`CLAUDE.md`, `AGENTS.md`, and similar AI-agent guidance files are **not committed**. They are gitignored to prevent personal tweaks from polluting the shared history.

The canonical content lives in `docs/agent-templates/CLAUDE.md.template`. **After cloning the repo, you must copy it:**

```bash
cp docs/agent-templates/CLAUDE.md.template ./CLAUDE.md
```

Now Claude Code will see `CLAUDE.md` at the repo root. Customize locally as needed; your changes stay on your machine.

## How to Use This with Claude Code

### Step 0 — Set up the repository

1. Create a new Git repository (GitHub or GitLab).
2. Copy the entire contents of this `leoflow/` directory into the repo root.
3. Commit and push as `chore: initial project skeleton`. Note that `CLAUDE.md` will NOT be in this commit because of `.gitignore`.
4. Copy the agent template into place locally:
   ```bash
   cp docs/agent-templates/CLAUDE.md.template ./CLAUDE.md
   ```
5. From the repo root, run `claude` (Claude Code CLI) to start the first session.

### Step 1 — Hand Claude the context

The first thing Claude Code should read is `CLAUDE.md`. Claude Code does this automatically if the file is at the repo root.

Your first message to Claude Code should be:

> Read `CLAUDE.md`, then read every file under `docs/adr/`, then read `prompts/phase-1-foundation.md`. After reading, summarize the project in your own words and outline how you would approach Phase 1. Do not write code yet.

This forces Claude to absorb the context before doing anything. Confirm the summary matches your understanding before moving on.

### Step 2 — Execute phases one at a time

Each phase has a dedicated prompt file. Run **one phase per Claude Code session** (or one major chunk per session if the phase is large).

The flow for each phase:

1. New Claude Code session.
2. Message: "Read `CLAUDE.md`, all ADRs, and `prompts/phase-N-*.md`. Execute Phase N. Stop and ask before making decisions that contradict any ADR."
3. Let Claude work. Review diffs in small batches. Reject ones that violate the principles.
4. At the end of the phase, run all acceptance criteria manually.
5. Commit with a descriptive message: `feat(phase-N): ...`.

### Step 3 — Course correction

If Claude Code goes off-track:

- **Stop the session.** Do not let it dig further.
- Identify which ADR or principle was violated.
- Start a fresh session and reference the specific document.
- Never edit the ADRs to match Claude's code. The ADRs are the source of truth.

## Estimated Timeline

| Phase | Description | Estimated time (1 engineer) |
|---|---|---|
| 1 | Foundation | 1-2 weeks |
| 2 | Control plane core | 2-3 weeks |
| 3 | Executor and agent | 2-3 weeks |
| 4 | XCom and logs | 1-2 weeks |
| 5 | UI integration | 1 week |
| 6 | Hardening and release | 2 weeks |
| **Total** | **MVP v0.1.0** | **9-13 weeks** |

These numbers assume a senior engineer working full-time with Claude Code as a pair-programming partner. Solo without AI assist, double.

## Critical Reminders

1. **English everywhere.** Code, comments, identifiers, commits, ADRs. Non-negotiable.
2. **Strict TDD.** Every line of production code preceded by a failing test. See ADR 0011. No exceptions.
3. **Go Report Card A+ from the first commit.** GoDocs on every exported identifier. Cyclomatic complexity ≤ 15. `make lint` clean. See ADR 0012.
4. **Scalar docs embedded in the server.** `/docs` route always matches the running version. See ADR 0013.
5. **Supply chain security from day one.** govulncheck + gosec + Trivy + CodeQL + Scorecard. See ADR 0014.
6. **Never modify ADRs without explicit decision review.** They encode the choices that took weeks of design.
7. **The Airflow API is the public surface for the MVP.** Don't innovate at this layer.
8. **Observability ships from day one.** No "we'll add metrics later."
9. **Coverage floors (per-package, CI-enforced):** Phase 1: 70%, Phase 2: 75%, Phase 3: 75%, Phase 4: 80%, Phase 5: 80%, Phase 6: 85%.
10. **Agent context files are gitignored.** Copy from `docs/agent-templates/` after cloning.

## Decisions Already Made (Do Not Re-litigate)

These decisions are sealed. Save energy:

- ✅ Go for control plane, agent, CLI
- ✅ Python only for the parser sidecar
- ✅ Postgres for metadata
- ✅ Redis for XCom (not Postgres, not shared volume — see discussion in design phase)
- ✅ Pod-per-task execution model
- ✅ DAG-as-Image (every DAG is a container)
- ✅ `leoflow.yaml` abstraction for image build
- ✅ Hybrid DAG authoring (Python parsed at compile time + YAML option)
- ✅ Static DAGs only in MVP (no dynamic mapping)
- ✅ Airflow 3.2.x UI compatibility
- ✅ JWT auth in MVP, OIDC interface ready for v1.1
- ✅ Postgres advisory locks for leader election
- ✅ OpenTelemetry + Prometheus + slog
- ✅ Apache 2.0 license
- ✅ **Strict TDD (red → green → refactor) — ADR 0011**
- ✅ **Go Report Card A+ floor + GoDocs on all exports — ADR 0012**
- ✅ **Scalar embedded for API docs at `/docs` — ADR 0013**
- ✅ **Supply chain stack: govulncheck + gosec + Trivy + CodeQL + Scorecard + cosign — ADR 0014**
- ✅ **OpenSSF Best Practices badge target after v0.1.0**
- ✅ **AI agent context files gitignored, copied from templates**

## Decisions Deliberately Deferred (Post-MVP)

- ⏸️ Optimized backfill
- ⏸️ UI scaling for 10k+ DAGs
- ⏸️ Dynamic task mapping
- ⏸️ OIDC implementation
- ⏸️ Mark success/failed manually
- ⏸️ Custom UI (replacing Airflow UI)

## Where to Ask for Help

- Architectural question → re-read ADRs. If unanswered, document in a new ADR draft.
- Stuck on Go specifics → standard channels (Go community, Stack Overflow).
- Stuck on K8s specifics → study the Airflow `KubernetesExecutor` source code first.
- Stuck on Airflow API compatibility → the OpenAPI spec at `docs/api/openapi.yaml` is the source of truth.

## Final Word

This package is the result of a deliberate design conversation. Every decision has a reason recorded in an ADR. Trust the ADRs. Execute the phases. Build the orchestrator that the data engineering community has been waiting for.

Welcome to Leoflow.
