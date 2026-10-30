package timeout_test

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	phase := os.Getenv("RUNNER_FIXTURE_PHASE")
	if phase == "setup" {
		time.Sleep(350 * time.Millisecond)
		fmt.Println("fixture setup complete")
	}
	code := m.Run()
	if phase == "teardown" {
		time.Sleep(350 * time.Millisecond)
		fmt.Println("fixture teardown complete")
	}
	os.Exit(code)
}

func TestFixture(t *testing.T) {
	t.Log("fixture test output")
	switch os.Getenv("RUNNER_FIXTURE_PHASE") {
	case "default-timeout":
		if got := flag.Lookup("test.timeout").Value.String(); got != "1h0m0s" {
			t.Fatalf("native timeout = %s, want 1h0m0s", got)
		}
	case "no-timeout":
		for _, arg := range os.Args[1:] {
			if strings.HasPrefix(arg, "-test.timeout") {
				t.Fatalf("unexpected injected timeout: %s", arg)
			}
		}
	case "timeout":
		time.Sleep(2 * time.Second)
	case "slow":
		time.Sleep(350 * time.Millisecond)
	}
}
