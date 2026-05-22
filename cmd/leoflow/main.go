// Command leoflow is the developer CLI for authoring and compiling DAGs.
package main

import (
	"os"

	"github.com/neochaotic/leoflow/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
