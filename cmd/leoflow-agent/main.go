// Command leoflow-agent runs inside the worker container and talks gRPC to the
// control plane. The full agent arrives in Phase 3; this entry point keeps the
// build and CI green.
package main

import "fmt"

func main() {
	fmt.Println("leoflow-agent is not yet implemented (arriving in Phase 3).")
}
