package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func unsetTimeoutEnv(t *testing.T) {
	t.Helper()
	t.Setenv("E2E_PACKAGE_TIMEOUT", "")
	if err := os.Unsetenv("E2E_PACKAGE_TIMEOUT"); err != nil {
		t.Fatal(err)
	}
}

func TestNativeTimeoutConfiguration(t *testing.T) {
	unsetTimeoutEnv(t)
	for _, tc := range []struct {
		name    string
		field   time.Duration
		env     string
		setEnv  bool
		want    time.Duration
		invalid bool
	}{
		{name: "zero caller", field: 0, want: 30 * time.Minute},
		{name: "caller", field: time.Hour, want: time.Hour},
		{name: "environment", field: time.Hour, env: "2m3s", setEnv: true, want: 123 * time.Second},
		{name: "negative caller", field: -time.Second, want: -time.Second},
		{name: "zero environment", field: time.Hour, env: "0", setEnv: true, want: 30 * time.Minute},
		{name: "negative environment", field: time.Hour, env: "-1s", setEnv: true, want: -time.Second},
		{name: "empty environment", setEnv: true, invalid: true},
		{name: "malformed environment", env: "forever", setEnv: true, invalid: true},
		{name: "overflow", env: "999999999999h", setEnv: true, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv("E2E_PACKAGE_TIMEOUT", tc.env)
			}
			r := &Runner{TestTimeout: tc.field}
			err := r.initialize()
			if (err != nil) != tc.invalid {
				t.Fatalf("initialize() = %v, invalid = %v", err, tc.invalid)
			}
			if !tc.invalid && r.TestTimeout != tc.want {
				t.Fatalf("timeout = %s, want %s", r.TestTimeout, tc.want)
			}
		})
	}
}

func writeTimeoutScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeTimeoutForwarding(t *testing.T) {
	unsetTimeoutEnv(t)
	for _, tc := range []struct {
		name  string
		field time.Duration
		env   string
		args  []string
		want  string
	}{
		{name: "default", want: "30m0s"},
		{name: "caller", field: time.Hour, want: "1h0m0s"},
		{name: "negative caller", field: -time.Second},
		{name: "negative environment", env: "-1s"},
		{name: "zero environment", field: time.Hour, env: "0", want: "30m0s"},
		{name: "negative with equals flag", field: -time.Second, args: []string{"-test.timeout=1h"}},
		{name: "negative with separate flag", env: "-1s", args: []string{"-test.timeout", "0"}},
		{name: "environment", field: time.Hour, env: "2m", want: "2m0s"},
		{name: "equals flag", env: "2m", args: []string{"-test.timeout=1h"}},
		{name: "separate flag", env: "2m", args: []string{"-test.timeout", "1h"}},
		{name: "disable", args: []string{"-test.timeout=0"}},
		{name: "separate disable", args: []string{"-test.timeout", "0"}},
		{name: "negative explicit", args: []string{"-test.timeout=-1s"}},
		{name: "repeated explicit unchanged", args: []string{"-test.timeout=1h", "-test.timeout", "0"}},
		{name: "invalid explicit unchanged", args: []string{"-test.timeout=forever"}},
		{name: "missing explicit value unchanged", args: []string{"-test.timeout"}},
		{name: "other flags unchanged", args: []string{"-test.run=TestFoo", "-test.timeout-extra=1h"}, want: "30m0s"},
		{name: "equals after separator", args: []string{"--", "-test.timeout=1h"}, want: "30m0s"},
		{name: "separate after separator", args: []string{"--", "-test.timeout", "1h"}, want: "30m0s"},
		{name: "explicit before separator", args: []string{"-test.timeout=1h", "--", "-test.timeout=0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("E2E_PACKAGE_TIMEOUT", tc.env)
			}
			dir := t.TempDir()
			converter := writeTimeoutScript(t, dir, "converter", "printf '%s\\n' \"$@\"\n")
			var stdout, stderr bytes.Buffer
			r := &Runner{
				Stdout: &stdout, Stderr: &stderr, Test2JSONBin: converter, TestTimeout: tc.field,
				Packages: []TestPackage{{Name: "first", Binary: "first-tests"}, {Name: "second", Binary: "second-tests"}},
			}
			if result := r.Run(append([]string{"exec"}, tc.args...)); result.ExitCode != 0 {
				t.Fatalf("Run() = %+v\n%s", result, &stderr)
			}
			var want strings.Builder
			for _, pkg := range r.Packages {
				args := make([]string, 0, 7+len(tc.args))
				args = append(args, "-t", "-p", pkg.Name, pkg.Binary)
				if tc.want != "" {
					args = append(args, "-test.timeout="+tc.want)
				}
				args = append(args, "-test.count=1", "-test.v=test2json")
				args = append(args, tc.args...)
				want.WriteString(strings.Join(args, "\n") + "\n")
			}
			if stdout.String() != want.String() {
				t.Fatalf("forwarded args = %q, want %q", stdout.String(), want.String())
			}
		})
	}
}

func buildTimeoutTool(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	binary := filepath.Join(dir, name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	buildArgs := append([]string{}, args[:len(args)-1]...)
	buildArgs = append(buildArgs, "-o", binary, args[len(args)-1])
	cmd := exec.CommandContext(ctx, "go", buildArgs...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, output)
	}
	return binary
}

// Exercise real self-exec -> gotestsum -> executor -> test2json -> Go binaries.
// Fixtures are finite; only this test harness imposes an external deadline.
func TestNativeTimeoutIntegration(t *testing.T) {
	dir := t.TempDir()
	gotestsum, err := exec.LookPath("gotestsum")
	if err != nil {
		gotestsum = buildTimeoutTool(t, dir, "gotestsum", "build", "gotest.tools/gotestsum")
	}
	converter := buildTimeoutTool(t, dir, "test2json", "build", "cmd/test2json")
	fixture := buildTimeoutTool(t, dir, "fixture", "test", "-c", "./testdata/timeout")
	helper := buildTimeoutTool(t, dir, "runner", "build", "./testdata/timeout-runner")
	t.Setenv("E2E_GOTESTSUM_BIN", gotestsum)
	t.Setenv("E2E_TEST2JSON_BIN", converter)
	t.Setenv("E2E_RESULTS_DIR", "results")
	unsetTimeoutEnv(t)

	run := func(t *testing.T, phases []string, field string, args ...string) (Result, []byte) {
		t.Helper()
		work := t.TempDir()
		specs := make([]string, 0, len(phases))
		for i, phase := range phases {
			name := fmt.Sprintf("pkg%d_%s", i, phase)
			binary := writeTimeoutScript(t, work, name, fmt.Sprintf(
				"export RUNNER_FIXTURE_PHASE=%q\nexec %q \"$@\"\n", phase, fixture))
			specs = append(specs, name+"="+binary)
		}
		t.Setenv("RUNNER_FIXTURE_PACKAGES", strings.Join(specs, ","))
		t.Setenv("RUNNER_FIXTURE_TIMEOUT", field)
		t.Setenv("ARTIFACTS", work)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, helper, args...)
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("test harness deadline exceeded: %v\n%s", ctx.Err(), output)
		}
		result := Result{JUnitFile: filepath.Join(work, "results", "junit.xml"), JSONFile: filepath.Join(work, "results", "log.jsonl")}
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("run fixture: %v\n%s", err, output)
			}
			result.ExitCode = exitErr.ExitCode()
		}
		data, err := os.ReadFile(result.JSONFile)
		if err != nil {
			t.Fatalf("read JSON: %v\n%s", err, output)
		}
		return result, data
	}

	for _, mode := range []string{"rhoai", "verbatim"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("E2E_JUNIT_REPORT_MODE", mode)
			for _, source := range []string{"caller", "environment"} {
				t.Run(source, func(t *testing.T) {
					field := "500ms"
					if source == "environment" {
						field = "1h"
						t.Setenv("E2E_PACKAGE_TIMEOUT", "500ms")
					}
					result, data := run(t, []string{"timeout", "slow", "slow", "pass"}, field)
					if result.ExitCode == 0 || !bytes.Contains(data, []byte("test timed out after 500ms")) {
						t.Fatalf("native timeout not reported: %+v\n%s", result, data)
					}
					assertNativeTimeoutReports(t, result, data)
				})
			}
		})
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "equals", args: []string{"-test.timeout=2s"}},
		{name: "separate", args: []string{"-test.timeout", "2s"}},
		{name: "zero", args: []string{"-test.timeout=0"}},
		{name: "separate zero", args: []string{"-test.timeout", "0"}},
		{name: "last explicit wins", args: []string{"-test.timeout=1ms", "-test.timeout", "2s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("E2E_PACKAGE_TIMEOUT", "50ms")
			result, data := run(t, []string{"slow"}, "1ms", tc.args...)
			if result.ExitCode != 0 || !bytes.Contains(data, []byte(`"Action":"pass","Package":"pkg0_slow","Test":"TestFixture"`)) {
				t.Fatalf("explicit native timeout override failed: %+v\n%s", result, data)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		field string
		env   string
		phase string
		args  []string
	}{
		{name: "default", phase: "default-timeout"},
		{name: "zero caller", field: "0", phase: "default-timeout"},
		{name: "negative caller", field: "-1s", phase: "no-timeout"},
		{name: "negative environment", env: "-1s", phase: "no-timeout"},
		{name: "zero environment", field: "1h", env: "0", phase: "default-timeout"},
		{name: "negative with equals flag", field: "-1s", phase: "slow", args: []string{"-test.timeout=2s"}},
		{name: "negative with separate flag", env: "-1s", phase: "slow", args: []string{"-test.timeout", "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("E2E_PACKAGE_TIMEOUT", tc.env)
			}
			result, data := run(t, []string{tc.phase}, tc.field, tc.args...)
			if result.ExitCode != 0 || !bytes.Contains(data, []byte(`"Action":"pass"`)) {
				t.Fatalf("zero/negative timeout failed: %+v\n%s", result, data)
			}
		})
	}
	for _, phase := range []string{"setup", "teardown"} {
		t.Run(phase+" outside m.Run", func(t *testing.T) {
			result, data := run(t, []string{phase}, "100ms")
			if result.ExitCode != 0 || !bytes.Contains(data, []byte("fixture "+phase+" complete")) {
				t.Fatalf("native alarm incorrectly bounded TestMain %s: %+v\n%s", phase, result, data)
			}
		})
	}
}

func assertNativeTimeoutReports(t *testing.T, result Result, data []byte) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	passed, failed := map[string]bool{}, map[string]bool{}
	for {
		var event struct{ Action, Package, Test string }
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("invalid JSON stream: %v", err)
		}
		if event.Test == "TestFixture" {
			if event.Action == "pass" {
				passed[event.Package] = true
			}
			if event.Action == "fail" {
				failed[event.Package] = true
			}
		}
	}
	if !failed["pkg0_timeout"] || !passed["pkg1_slow"] || !passed["pkg2_slow"] || !passed["pkg3_pass"] {
		t.Errorf("native failure/continuation/fresh budgets: passed=%v failed=%v\n%s", passed, failed, data)
	}
	report, err := os.ReadFile(result.JUnitFile)
	if err != nil {
		t.Fatal(err)
	}
	var suites struct {
		Suites []struct {
			Cases []struct {
				Name    string  `xml:"name,attr"`
				Failure *string `xml:"failure"`
			} `xml:"testcase"`
		} `xml:"testsuite"`
	}
	if err := xml.Unmarshal(report, &suites); err != nil {
		t.Fatal(err)
	}
	failures, passes := 0, 0
	panicReported := false
	for _, suite := range suites.Suites {
		for _, tc := range suite.Cases {
			// gotestsum may attach the panic to TestMain rather than the test.
			if tc.Failure != nil && strings.Contains(*tc.Failure, "test timed out after 500ms") {
				panicReported = true
			}
			if !strings.Contains(tc.Name, "TestFixture") {
				continue
			}
			if tc.Failure != nil {
				failures++
			} else {
				passes++
			}
		}
	}
	if failures != 1 || passes != 3 || !panicReported {
		t.Errorf("JUnit failures/passes = %d/%d, want 1/3 with native panic: %s", failures, passes, report)
	}
}
