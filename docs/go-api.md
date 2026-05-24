# Go packages (GoDocs)

Leoflow's control plane, agent, and CLI are Go. Every exported identifier carries
a GoDoc (Go Report Card A+ is the quality floor).

- **Browse online:** <https://pkg.go.dev/github.com/neochaotic/leoflow>
- **Locally:**
  ```bash
  go doc ./internal/scheduler
  go doc ./internal/cli Dev          # or any package/identifier
  # full browsable site:
  go install golang.org/x/pkgsite/cmd/pkgsite@latest && pkgsite .
  ```

Key packages: `internal/api` (HTTP), `internal/scheduler` (state machine),
`internal/executor` (K8s/subprocess), `internal/dispatch`, `internal/agentrpc`
(agent gRPC), `internal/storage` (Postgres/Redis), `internal/cli` (the `leoflow`
CLI), `internal/domain` (core types).
