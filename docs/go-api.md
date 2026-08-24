# Go packages (GoDocs)

Leoflow's control plane, agent, and CLI are Go. Every exported identifier carries
a GoDoc (Go Report Card A+ is the quality floor), and each symbol links to its
source on GitHub.

One page per package keeps each reference a readable length. Pick a package:

<div class="grid cards" markdown>

- [`pkg/client`](go/pkg/client.md) — the typed `/api/v2` client (**public**)
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

## The typed `/api/v2` client (`pkg/client`)

`pkg/client` is the one **public** package: a typed client for the control plane's
`/api/v2`, generated from the OpenAPI spec ([ADR 0050](adr/0050-mcp-server.md) D8).
The CLI and the [MCP server](mcp.md) both route through it, so it is the supported
way to call Leoflow from your own Go code.

`New(baseURL, token)` builds a client. When `token` is non-empty every request
carries `Authorization: Bearer <token>` (pass a JWT from
`leoflow auth create-token`); an empty token leaves requests unauthenticated, for
loopback dev.

```go
package main

import (
	"context"
	"fmt"
	"log"

	apiclient "github.com/neochaotic/leoflow/pkg/client"
)

func main() {
	ctx := context.Background()

	// token: a Leoflow JWT (e.g. from `leoflow auth create-token`); "" for loopback dev.
	token := "" // paste a JWT, or read it from the environment
	c, err := apiclient.New("http://localhost:8088", token)
	if err != nil {
		log.Fatal(err)
	}

	resp, err := c.ListDagsWithResponse(ctx, &apiclient.ListDagsParams{})
	if err != nil {
		log.Fatal(err)
	}
	if resp.JSON200 == nil {
		log.Fatalf("control plane returned %d", resp.StatusCode())
	}
	for _, d := range *resp.JSON200.Dags {
		fmt.Printf("%s (paused=%v)\n", *d.DagId, *d.IsPaused)
	}
}
```

Every operation has a `…WithResponse` method whose result carries the typed
`JSON200` body (and the raw `StatusCode()`); optional fields are pointers, so
guard with a nil check or the generated helpers.

## Browse locally
```bash
go doc ./internal/scheduler
go doc ./internal/cli Dev                # any package/identifier
go install golang.org/x/pkgsite/cmd/pkgsite@latest && pkgsite .   # full browsable site
```

!!! note
    Once the module is public, the same docs are on
    [pkg.go.dev](https://pkg.go.dev/github.com/neochaotic/leoflow).
