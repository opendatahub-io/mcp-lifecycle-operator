package main

import (
	"os"

	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/runner"
)

func main() {
	os.Exit(runner.New().Run(os.Args[1:]).ExitCode)
}
