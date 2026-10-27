package main

import (
	"os"
	"time"

	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/runner"
)

func main() {
	r := runner.New()
	var err error
	r.Packages, err = runner.ParsePackages(os.Getenv("RUNNER_FIXTURE_PACKAGES"))
	if err != nil {
		panic(err)
	}
	// Configure only the orchestrator so tests verify propagation across self-exec.
	if value := os.Getenv("RUNNER_FIXTURE_TIMEOUT"); value != "" && (len(os.Args) < 2 || os.Args[1] != "exec") {
		r.TestTimeout, err = time.ParseDuration(value)
		if err != nil {
			panic(err)
		}
	}
	os.Exit(r.Run(os.Args[1:]).ExitCode)
}
