# Go packages (GoDocs)

Leoflow's control plane, agent, and CLI are Go. Every exported identifier carries
a GoDoc (Go Report Card A+ is the quality floor), and each symbol links to its
source on GitHub.

One page per package keeps each reference a readable length. Pick a package:

<div class="grid cards" markdown>

- [`internal/domain`](go/internal/domain.md) — core types (DAG, Task, Run, …)
- [`internal/scheduler`](go/internal/scheduler.md) — the state machine
- [`internal/executor`](go/internal/executor.md) — K8s / subprocess executors
- [`internal/dispatch`](go/internal/dispatch.md) — executor routing
- [`internal/agent`](go/internal/agent.md) — the in-container agent
- [`internal/agentrpc`](go/internal/agentrpc.md) — agent gRPC
- [`internal/storage`](go/internal/storage.md) — Postgres / Redis
- [`internal/auth`](go/internal/auth.md) — JWT + RBAC
- [`internal/config`](go/internal/config.md) — configuration
- [`internal/cli`](go/internal/cli.md) — the `leoflow` CLI
- [`internal/api`](go/internal/api.md) — HTTP handlers

</div>

## Browse locally
```bash
go doc ./internal/scheduler
go doc ./internal/cli Dev                # any package/identifier
go install golang.org/x/pkgsite/cmd/pkgsite@latest && pkgsite .   # full browsable site
```

!!! note
    Once the module is public, the same docs are on
    [pkg.go.dev](https://pkg.go.dev/github.com/neochaotic/leoflow).
