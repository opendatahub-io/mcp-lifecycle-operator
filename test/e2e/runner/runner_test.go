package runner_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/runner"
)

func TestRunBuildsCorrectCommand(t *testing.T) {
	tmpDir := t.TempDir()

	gotestsumBin := filepath.Join(tmpDir, "gotestsum")
	if err := os.WriteFile(gotestsumBin, []byte("#!/bin/sh\necho \"$@\" > "+filepath.Join(tmpDir, "gotestsum.args")+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	test2jsonBin := filepath.Join(tmpDir, "test2json")
	if err := os.WriteFile(test2jsonBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	testBinary := filepath.Join(tmpDir, "e2e-tests")
	if err := os.WriteFile(testBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	artifactsDir := filepath.Join(tmpDir, "artifacts")

	t.Setenv("E2E_GOTESTSUM_BIN", gotestsumBin)
	t.Setenv("E2E_TEST2JSON_BIN", test2jsonBin)
	t.Setenv("E2E_TEST_BINARY", testBinary)
	t.Setenv("ARTIFACTS", artifactsDir)
	t.Setenv("E2E_RESULTS_DIR", "results")
	t.Setenv("E2E_COUNT", "3")
	t.Setenv("GO_TEST_VERBOSITY", "standard-verbose")
	t.Setenv("E2E_JUNIT_SUITE_NAME", "mysuite")
	t.Setenv("E2E_JUNIT_CLASS_NAME", "myclass")

	var stdoutBuf, stderrBuf bytes.Buffer
	result := runner.New().WithStdout(&stdoutBuf).WithStderr(&stderrBuf).Run([]string{"-test.timeout=5m", "-test.run=TestFoo"})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}

	argsBytes, err := os.ReadFile(filepath.Join(tmpDir, "gotestsum.args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(argsBytes))

	resultsDir := filepath.Join(artifactsDir, "results")
	expects := []string{
		"--raw-command",
		"--junitfile " + filepath.Join(resultsDir, "junit.xml"),
		"--junitfile-testsuite-name mysuite",
		"--junitfile-testcase-classname myclass",
		"--jsonfile " + filepath.Join(resultsDir, "log.jsonl"),
		"--format standard-verbose",
		"-- " + test2jsonBin + " -t " + testBinary,
		"-test.count=3",
		"-test.v=test2json",
		"-test.timeout=5m",
		"-test.run=TestFoo",
	}

	for _, exp := range expects {
		if !strings.Contains(got, exp) {
			t.Errorf("expected args to contain %q\ngot: %s", exp, got)
		}
	}

	if _, err := os.Stat(resultsDir); os.IsNotExist(err) {
		t.Error("expected results dir to be created")
	}

	if result.JUnitFile != filepath.Join(resultsDir, "junit.xml") {
		t.Errorf("expected JUnitFile %q, got %q", filepath.Join(resultsDir, "junit.xml"), result.JUnitFile)
	}
	if result.JSONFile != filepath.Join(resultsDir, "log.jsonl") {
		t.Errorf("expected JSONFile %q, got %q", filepath.Join(resultsDir, "log.jsonl"), result.JSONFile)
	}

	logs := stderrBuf.String()
	expectedLogs := []string{
		"[e2e-run] junit: " + filepath.Join(resultsDir, "junit.xml"),
		"[e2e-run] jsonl: " + filepath.Join(resultsDir, "log.jsonl"),
	}
	for _, exp := range expectedLogs {
		if !strings.Contains(logs, exp) {
			t.Errorf("expected log output to contain %q\ngot: %s", exp, logs)
		}
	}
}
