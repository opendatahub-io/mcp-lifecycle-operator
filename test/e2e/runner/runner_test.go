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

	selfBin := filepath.Join(tmpDir, "e2e-run")
	if err := os.WriteFile(selfBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	artifactsDir := filepath.Join(tmpDir, "artifacts")

	t.Setenv("E2E_GOTESTSUM_BIN", gotestsumBin)
	t.Setenv("ARTIFACTS", artifactsDir)
	t.Setenv("E2E_RESULTS_DIR", "results")
	t.Setenv("E2E_COUNT", "3")
	t.Setenv("GO_TEST_VERBOSITY", "standard-verbose")
	t.Setenv("E2E_JUNIT_PROJECT_NAME", "myproject")

	packages := []runner.TestPackage{
		{Name: "mypackage", Binary: "/tmp/e2e-tests"},
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	result := runner.New().
		WithStdout(&stdoutBuf).
		WithStderr(&stderrBuf).
		WithSelf(selfBin).
		WithPackages(packages).
		Run([]string{"-test.timeout=5m", "-test.run=TestFoo"})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstderr: %s", result.ExitCode, stderrBuf.String())
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
		"--junitfile-project-name myproject",
		"--jsonfile " + filepath.Join(resultsDir, "log.jsonl"),
		"--format standard-verbose",
		"-- " + selfBin + " exec",
		"-test.timeout=5m",
		"-test.run=TestFoo",
	}

	for _, exp := range expects {
		if !strings.Contains(got, exp) {
			t.Errorf("expected args to contain %q\ngot: %s", exp, got)
		}
	}

	// Run() must NOT inject -test.count or -test.v=test2json -- Exec() does that.
	if strings.Contains(got, "-test.count=") {
		t.Errorf("Run() must not inject -test.count into gotestsum args\ngot: %s", got)
	}
	if strings.Contains(got, "-test.v=test2json") {
		t.Errorf("Run() must not inject -test.v=test2json into gotestsum args\ngot: %s", got)
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

func TestExecRunsTest2JSONForEachPackage(t *testing.T) {
	tmpDir := t.TempDir()

	argsFile := filepath.Join(tmpDir, "test2json.args")
	test2jsonBin := filepath.Join(tmpDir, "test2json")
	script := "#!/bin/sh\necho \"$@\" >> " + argsFile + "\n"
	if err := os.WriteFile(test2jsonBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	binary1 := filepath.Join(tmpDir, "e2e-tests")
	binary2 := filepath.Join(tmpDir, "integration-tests")
	for _, b := range []string{binary1, binary2} {
		if err := os.WriteFile(b, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("E2E_TEST2JSON_BIN", test2jsonBin)
	t.Setenv("E2E_COUNT", "2")

	prev := runner.TestPackages
	runner.TestPackages = "e2e=" + binary1 + ",integration=" + binary2
	t.Cleanup(func() { runner.TestPackages = prev })

	var stdoutBuf, stderrBuf bytes.Buffer
	result := runner.New().
		WithStdout(&stdoutBuf).
		WithStderr(&stderrBuf).
		Run([]string{"exec", "-test.run=TestFoo"})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstderr: %s", result.ExitCode, stderrBuf.String())
	}

	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(argsBytes)

	// Each package line: "-t -p <name> <binary> -test.count=2 -test.v=test2json -test.run=TestFoo"
	expects := []string{
		"-t -p e2e " + binary1 + " -test.count=2 -test.v=test2json -test.run=TestFoo",
		"-t -p integration " + binary2 + " -test.count=2 -test.v=test2json -test.run=TestFoo",
	}
	for _, exp := range expects {
		if !strings.Contains(got, exp) {
			t.Errorf("expected test2json args to contain %q\ngot: %s", exp, got)
		}
	}
}

func TestParsePackages(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []runner.TestPackage
		wantErr bool
	}{
		{
			name:    "empty string fails",
			input:   "",
			wantErr: true,
		},
		{
			name:  "single package",
			input: "e2e=/e2e/e2e-tests",
			want:  []runner.TestPackage{{Name: "e2e", Binary: "/e2e/e2e-tests"}},
		},
		{
			name:  "multiple packages",
			input: "e2e=/e2e/e2e-tests,integration=/e2e/integration-tests",
			want: []runner.TestPackage{
				{Name: "e2e", Binary: "/e2e/e2e-tests"},
				{Name: "integration", Binary: "/e2e/integration-tests"},
			},
		},
		{
			name:    "malformed no equals sign",
			input:   "e2e-tests",
			wantErr: true,
		},
		{
			name:    "malformed empty name",
			input:   "=/e2e/e2e-tests",
			wantErr: true,
		},
		{
			name:    "malformed empty binary",
			input:   "e2e=",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runner.ParsePackages(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("expected %d packages, got %d: %+v", len(tc.want), len(got), got)
			}
			for i, pkg := range got {
				if pkg != tc.want[i] {
					t.Errorf("package[%d]: expected %+v, got %+v", i, tc.want[i], pkg)
				}
			}
		})
	}
}
