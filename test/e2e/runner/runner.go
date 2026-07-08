package runner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	prefix              = "[e2e-run] "
	defaultTestBinary   = "/e2e/e2e-tests"
	defaultTest2JSONBin = "/usr/local/bin/test2json"
	defaultArtifactsDir = "/artifacts"
	defaultResultsDir   = "e2e-results"
	defaultGotestsumBin = "gotestsum"
)

type Result struct {
	JUnitFile string
	JSONFile  string
	ExitCode  int
}

type Runner struct {
	stdout io.Writer
	stderr io.Writer
}

func New() *Runner {
	return &Runner{stdout: os.Stdout, stderr: os.Stderr}
}

func (r *Runner) WithStdout(w io.Writer) *Runner {
	r.stdout = w
	return r
}

func (r *Runner) WithStderr(w io.Writer) *Runner {
	r.stderr = w
	return r
}

func (r *Runner) Run(args []string) Result {
	testBinary := envOr("E2E_TEST_BINARY", defaultTestBinary)
	test2jsonBin := envOr("E2E_TEST2JSON_BIN", defaultTest2JSONBin)
	artifactsDir := envOr("ARTIFACTS", defaultArtifactsDir)
	resultsDir := filepath.Join(artifactsDir, envOr("E2E_RESULTS_DIR", defaultResultsDir))
	gotestsumBin := envOr("E2E_GOTESTSUM_BIN", defaultGotestsumBin)
	goTestVerbosity := envOr("GO_TEST_VERBOSITY", "testname")
	testCount := envOr("E2E_COUNT", "1")
	junitSuiteName := envOr("E2E_JUNIT_SUITE_NAME", "relative")
	junitClassName := envOr("E2E_JUNIT_CLASS_NAME", "relative")

	junitFile := filepath.Join(resultsDir, "junit.xml")
	jsonFile := filepath.Join(resultsDir, "log.jsonl")

	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		r.logf("failed to create results dir %s: %v", resultsDir, err)
		return Result{ExitCode: 1}
	}

	testArgs := make([]string, 0, 2+len(args))
	testArgs = append(testArgs, "-test.count="+testCount, "-test.v=test2json")
	testArgs = append(testArgs, args...)

	cmdArgs := make([]string, 0, 15+len(testArgs))
	cmdArgs = append(cmdArgs,
		"--raw-command",
		"--junitfile", junitFile,
		"--junitfile-testsuite-name", junitSuiteName,
		"--junitfile-testcase-classname", junitClassName,
		"--jsonfile", jsonFile,
		"--format", goTestVerbosity,
		"--",
		test2jsonBin, "-t", testBinary,
	)
	cmdArgs = append(cmdArgs, testArgs...)

	r.logf("running: %s %s", gotestsumBin, strings.Join(cmdArgs, " "))

	cmd := exec.Command(gotestsumBin, cmdArgs...)
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr
	cmd.Stdin = os.Stdin

	result := Result{JUnitFile: junitFile, JSONFile: jsonFile}

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			r.logf("junit: %s", junitFile)
			r.logf("jsonl: %s", jsonFile)
			return result
		}
		r.logf("failed to run gotestsum: %v", err)
		result.ExitCode = 1
		return result
	}

	r.logf("junit: %s", junitFile)
	r.logf("jsonl: %s", jsonFile)
	return result
}

func (r *Runner) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.stderr, prefix+format+"\n", args...)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
