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
	defaultComponent    = "mcp-lifecycle-operator"
	defaultTest2JSONBin = "/usr/local/bin/test2json"
	defaultArtifactsDir = "/artifacts"
	defaultResultsDir   = "e2e-results"
	defaultGotestsumBin = "gotestsum"
)

// TestPackages is set at build time via -ldflags:
//
//	-X 'github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/runner.TestPackages=e2e=/e2e/e2e-tests'
//
// Format: "name=binary,name2=binary2".
//
//nolint:gochecknoglobals // set via ldflags at build time
var TestPackages string

// TestPackage pairs a Go package name with its pre-compiled test binary.
type TestPackage struct {
	Name   string // e.g. "e2e" -- used as test2json -p value
	Binary string // e.g. "/e2e/e2e-tests"
}

// ParsePackages parses a comma-separated list of "name=binary" pairs.
// Format: "name=binary,name2=binary2" e.g. "e2e=/e2e/e2e-tests"
func ParsePackages(s string) ([]TestPackage, error) {
	if s == "" {
		return nil, fmt.Errorf("testPackages not set (must be set via -ldflags at build time)")
	}
	pairs := strings.Split(s, ",")
	pkgs := make([]TestPackage, 0, len(pairs))
	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid package spec %q: expected name=binary", pair)
		}
		pkgs = append(pkgs, TestPackage{Name: parts[0], Binary: parts[1]})
	}
	return pkgs, nil
}

// Result holds the output paths and exit code from Run.
type Result struct {
	JUnitFile string
	JSONFile  string
	ExitCode  int
}

// Runner executes e2e tests via gotestsum / test2json.
type Runner struct {
	Stdout       io.Writer
	Stderr       io.Writer
	selfPath     string
	Packages     []TestPackage
	ArtifactsDir string
	ResultsDir   string
	GotestsumBin string
	Verbosity    string
	Test2JSONBin string
	TestCount    string
	Component    string
	RepoAbbr     string
	ReportMode   string
}

// New returns an uninitialized, caller-configurable Runner.
func New() *Runner {
	return &Runner{}
}

// Run routes between orchestrator and executor modes.
// When args[0] is "exec", it streams test2json output for gotestsum.
// Otherwise it launches gotestsum with --raw-command pointing back at itself.
//
// Process tree (orchestrator invokes itself as executor):
//
//	e2e-run -test.run=TestFoo
//	 └─ gotestsum --raw-command ... -- e2e-run exec -test.run=TestFoo
//	     └─ e2e-run exec -test.run=TestFoo
//	         ├─ test2json -t -p e2e /e2e/e2e-tests -test.count=1 ...
//	         └─ test2json -t -p lifecycle /e2e/lifecycle-tests -test.count=1 ...
func (r *Runner) Run(args []string) Result {
	if err := r.initialize(); err != nil {
		r.logf("invalid runner configuration: %v", err)
		return Result{ExitCode: 1}
	}

	if err := r.initPackages(); err != nil {
		r.logf("invalid testPackages: %v", err)
		return Result{ExitCode: 1}
	}

	if len(args) > 0 && args[0] == "exec" {
		return Result{ExitCode: r.execPackages(args[1:])}
	}

	return r.orchestrate(args)
}

func (r *Runner) initialize() error {
	if r.Stdout == nil {
		r.Stdout = os.Stdout
	}
	if r.Stderr == nil {
		r.Stderr = os.Stderr
	}
	if r.selfPath == "" {
		r.selfPath = os.Args[0]
	}
	if r.ArtifactsDir == "" {
		r.ArtifactsDir = defaultArtifactsDir
	}
	if r.ResultsDir == "" {
		r.ResultsDir = defaultResultsDir
	}
	if r.GotestsumBin == "" {
		r.GotestsumBin = defaultGotestsumBin
	}
	if r.Verbosity == "" {
		r.Verbosity = "testname"
	}
	if r.Test2JSONBin == "" {
		r.Test2JSONBin = defaultTest2JSONBin
	}
	if r.TestCount == "" {
		r.TestCount = "1"
	}
	if r.Component == "" {
		r.Component = defaultComponent
	}
	if r.ReportMode == "" {
		r.ReportMode = "rhoai"
	}

	r.ArtifactsDir = envOr("ARTIFACTS", r.ArtifactsDir)
	r.ResultsDir = envOr("E2E_RESULTS_DIR", r.ResultsDir)
	r.GotestsumBin = envOr("E2E_GOTESTSUM_BIN", r.GotestsumBin)
	r.Verbosity = envOr("E2E_GO_TEST_VERBOSITY", r.Verbosity)
	r.Component = envOr("E2E_COMPONENT", r.Component)
	if r.RepoAbbr == "" {
		r.RepoAbbr = deriveRepoAbbr(r.Component)
	}
	r.RepoAbbr = envOr("E2E_REPO_ABBR", r.RepoAbbr)
	r.ReportMode = envOr("E2E_JUNIT_REPORT_MODE", r.ReportMode)
	r.Test2JSONBin = envOr("E2E_TEST2JSON_BIN", r.Test2JSONBin)
	r.TestCount = envOr("E2E_COUNT", r.TestCount)

	reportMode, err := parseJUnitReportMode(r.ReportMode)
	if err != nil {
		return err
	}
	r.ReportMode = string(reportMode)
	return nil
}

func (r *Runner) initPackages() error {
	if r.Packages != nil {
		return nil
	}
	pkgs, err := ParsePackages(TestPackages)
	if err != nil {
		return err
	}
	r.Packages = pkgs
	return nil
}

func (r *Runner) orchestrate(args []string) Result {
	reportConfig := junitReportConfig{
		mode:      junitReportMode(r.ReportMode),
		component: r.Component,
		repoAbbr:  r.RepoAbbr,
	}

	resultsDir := filepath.Join(r.ArtifactsDir, r.ResultsDir)
	junitFile := filepath.Join(resultsDir, "junit.xml")
	jsonFile := filepath.Join(resultsDir, "log.jsonl")

	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		r.logf("failed to create results dir %s: %v", resultsDir, err)
		return Result{ExitCode: 1}
	}

	selfPath := r.selfPath
	if selfPath == "" {
		selfPath = os.Args[0]
	}

	cmdArgs := make([]string, 0, 10+len(args))
	cmdArgs = append(cmdArgs,
		"--raw-command",
		"--junitfile", junitFile,
		"--junitfile-project-name", r.Component,
		"--jsonfile", jsonFile,
		"--format", r.Verbosity,
		"--",
		selfPath, "exec",
	)
	cmdArgs = append(cmdArgs, args...)

	r.logf("running: %s %s", r.GotestsumBin, strings.Join(cmdArgs, " "))

	cmd := exec.Command(r.GotestsumBin, cmdArgs...)
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	cmd.Stdin = os.Stdin

	result := Result{JUnitFile: junitFile, JSONFile: jsonFile}

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			r.logf("failed to run gotestsum: %v", err)
			result.ExitCode = 1
			return result
		}
	}

	if _, err := os.Stat(junitFile); os.IsNotExist(err) {
		if result.ExitCode == 0 {
			r.logf("gotestsum succeeded but JUnit report is missing: %s", junitFile)
			result.ExitCode = 1
		}
		return result
	} else if err != nil {
		r.logf("failed to inspect JUnit report %s: %v", junitFile, err)
		result.ExitCode = 1
		return result
	}

	if err := rewriteJUnitReport(junitFile, reportConfig); err != nil {
		r.logf("failed to rewrite JUnit report: %v", err)
		if result.ExitCode == 0 {
			result.ExitCode = 1
		}
	}

	r.logf("junit: %s", junitFile)
	r.logf("jsonl: %s", jsonFile)
	return result
}

func deriveRepoAbbr(component string) string {
	parts := strings.Split(component, "-")
	var abbreviation strings.Builder
	abbreviation.WriteString(parts[0])
	for _, part := range parts[1:] {
		if part != "" {
			_ = abbreviation.WriteByte(part[0])
		}
	}
	return abbreviation.String()
}

func (r *Runner) execPackages(args []string) int {
	testArgs := make([]string, 0, 2+len(args))
	testArgs = append(testArgs, "-test.count="+r.TestCount, "-test.v=test2json")
	testArgs = append(testArgs, args...)

	worstCode := 0

	for _, pkg := range r.Packages {
		cmdArgs := make([]string, 0, 4+len(testArgs))
		cmdArgs = append(cmdArgs, "-t", "-p", pkg.Name, pkg.Binary)
		cmdArgs = append(cmdArgs, testArgs...)

		r.logf("running: %s %s", r.Test2JSONBin, strings.Join(cmdArgs, " "))

		cmd := exec.Command(r.Test2JSONBin, cmdArgs...)
		cmd.Stdout = r.Stdout
		cmd.Stderr = r.Stderr

		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				code := exitErr.ExitCode()
				if code <= 0 {
					code = 1
				}
				if code > worstCode {
					worstCode = code
				}
			} else {
				r.logf("failed to run test2json for package %s: %v", pkg.Name, err)
				if worstCode == 0 {
					worstCode = 1
				}
			}
		}
	}

	return worstCode
}

func (r *Runner) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.Stderr, prefix+format+"\n", args...)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
