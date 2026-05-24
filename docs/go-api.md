# Go packages (GoDocs)

Leoflow's control plane, agent, and CLI are Go. Every exported identifier carries
a GoDoc (Go Report Card A+ is the quality floor), and each symbol links to its
source on GitHub.

[:material-book-open-variant: Full Go reference (all packages)](go/reference.md){ .md-button .md-button--primary }

Key packages: `internal/api` (HTTP), `internal/scheduler` (state machine),
`internal/executor` (K8s/subprocess), `internal/dispatch`, `internal/agentrpc`
(agent gRPC), `internal/storage` (Postgres/Redis), `internal/cli` (the `leoflow`
CLI), `internal/domain` (core types).

## Browse locally
```bash
go doc ./internal/scheduler
go doc ./internal/cli Dev                # any package/identifier
go install golang.org/x/pkgsite/cmd/pkgsite@latest && pkgsite .   # full browsable site
```

!!! note
    Once the module is public, the same docs are on
    [pkg.go.dev](https://pkg.go.dev/github.com/neochaotic/leoflow).
