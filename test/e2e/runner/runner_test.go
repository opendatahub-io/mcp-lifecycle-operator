package runner_test

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/runner"
)

type junitSuites struct {
	XMLName    xml.Name        `xml:"testsuites"`
	Name       string          `xml:"name,attr"`
	Tests      int             `xml:"tests,attr"`
	Failures   int             `xml:"failures,attr"`
	Skipped    int             `xml:"skipped,attr"`
	Time       float64         `xml:"time,attr"`
	Properties []junitProperty `xml:"properties>property"`
	Suites     []junitSuite    `xml:"testsuite"`
}

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     float64     `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      float64       `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure"`
	Skipped   *struct{}     `xml:"skipped"`
	Stdout    string        `xml:"system-out"`
	Stderr    string        `xml:"system-err"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

const representativeJUnit = `<testsuites name="gotestsum" tests="4" failures="1" skipped="1" time="4.0">
<testsuite name="tests/e2e" tests="1" failures="0" skipped="0" time="1.0"><testcase classname="e2e" name="tests/e2e/TestMCPServer" time="1.0"><system-out>e2e stdout</system-out><system-err>e2e stderr</system-err></testcase></testsuite>
<testsuite name="tests/upgrade" tests="1" failures="1" skipped="0" time="2.0"><testcase classname="upgrade" name="tests/upgrade/TestUpgrade" time="2.0"><failure message="upgrade failed">failure output</failure></testcase></testsuite>
<testsuite name="tests/integration/mcpgateway" tests="2" failures="0" skipped="1" time="1.0"><testcase classname="mcpgateway" name="tests/integration/mcpgateway/TestGateway" time="0.5"><system-out>gateway stdout</system-out><system-err>gateway stderr</system-err></testcase><testcase classname="mcpgateway" name="tests/integration/mcpgateway/TestGatewayHealth" time="0.5"><skipped/></testcase></testsuite>
</testsuites>`

func runJUnitReportWithIdentity(t *testing.T, codeComponent, codeRepoAbbr string) (runner.Result, junitSuites, string) {
	return runJUnitReportWithModesAndIdentity(t, "verbatim", codeComponent, codeRepoAbbr)
}

func runJUnitReportWithModes(t *testing.T, reportMode, repoAbbr string) (runner.Result, junitSuites, string) {
	return runJUnitReportWithModesAndIdentity(t, reportMode, "mcp-lifecycle-operator", repoAbbr)
}

func runJUnitReportWithModesAndIdentity(t *testing.T, reportMode, codeComponent, codeRepoAbbr string) (runner.Result, junitSuites, string) {
	t.Helper()
	tmpDir := t.TempDir()
	invoked := filepath.Join(tmpDir, "gotestsum.invoked")
	gotestsumBin := filepath.Join(tmpDir, "gotestsum")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" > %q
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--junitfile" ]; then junit="$2"; shift 2; continue; fi
  shift
done
mkdir -p "$(dirname "$junit")"
printf '%%s' %q > "$junit"
`, invoked, representativeJUnit)
	if err := os.WriteFile(gotestsumBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := runner.New()
	r.GotestsumBin = gotestsumBin
	r.ArtifactsDir = filepath.Join(tmpDir, "artifacts")
	r.ResultsDir = "results"
	r.Component = "mcp-lifecycle-operator"
	r.RepoAbbr = ""
	r.ReportMode = reportMode
	if codeComponent != "" {
		r.Component = codeComponent
	}
	if codeRepoAbbr != "" {
		r.RepoAbbr = codeRepoAbbr
	}
	r.Packages = []runner.TestPackage{{Name: "e2e", Binary: "/tmp/e2e-tests"}}
	result := r.Run(nil)
	var suites junitSuites
	if result.JUnitFile != "" {
		data, err := os.ReadFile(result.JUnitFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := xml.Unmarshal(data, &suites); err != nil {
			t.Fatal(err)
		}
	}
	invocation, _ := os.ReadFile(invoked)
	return result, suites, string(invocation)
}

func TestRunnerIdentityDefaultsToMCPLO(t *testing.T) {
	result, suites, invocation := runJUnitReportWithIdentity(t, "", "")
	if result.ExitCode != 0 {
		t.Fatalf("expected fake gotestsum to succeed, got %d", result.ExitCode)
	}
	if suites.Name != "mcp-lifecycle-operator" {
		t.Fatalf("expected default component, got %q", suites.Name)
	}
	properties := map[string]string{}
	for _, property := range suites.Properties {
		properties[property.Name] = property.Value
	}
	if properties["repo"] != "mcplo" {
		t.Fatalf("expected default derived repo abbreviation, got %q", properties["repo"])
	}
	if !strings.Contains(invocation, "--junitfile-project-name mcp-lifecycle-operator") {
		t.Fatalf("expected gotestsum project name from default component, got %q", invocation)
	}
}

func TestRunnerIdentityUsesCodeConfigurationAndDerivedRepoAbbreviation(t *testing.T) {
	result, suites, invocation := runJUnitReportWithIdentity(t, "custom-component", "")
	if result.ExitCode != 0 {
		t.Fatalf("expected fake gotestsum to succeed, got %d", result.ExitCode)
	}
	if suites.Name != "custom-component" {
		t.Fatalf("expected code-configured component, got %q", suites.Name)
	}
	properties := map[string]string{}
	for _, property := range suites.Properties {
		properties[property.Name] = property.Value
	}
	if properties["repo"] != "customc" {
		t.Fatalf("expected repo abbreviation derived from component, got %q", properties["repo"])
	}
	if !strings.Contains(invocation, "--junitfile-project-name custom-component") {
		t.Fatalf("expected gotestsum project name from component, got %q", invocation)
	}
}

func TestRunnerIdentityEnvironmentOverridesCodeConfiguration(t *testing.T) {
	t.Setenv("E2E_COMPONENT", "env-component")
	t.Setenv("E2E_REPO_ABBR", "env-repo")
	result, suites, invocation := runJUnitReportWithIdentity(t, "code-component", "code-repo")
	if result.ExitCode != 0 {
		t.Fatalf("expected fake gotestsum to succeed, got %d", result.ExitCode)
	}
	if suites.Name != "env-component" {
		t.Fatalf("expected component environment override, got %q", suites.Name)
	}
	properties := map[string]string{}
	for _, property := range suites.Properties {
		properties[property.Name] = property.Value
	}
	if properties["repo"] != "env-repo" {
		t.Fatalf("expected repo environment override, got %q", properties["repo"])
	}
	if !strings.Contains(invocation, "--junitfile-project-name env-component") {
		t.Fatalf("expected gotestsum project name from resolved component, got %q", invocation)
	}
}

func TestRunnerIdentityUsesExplicitRepoAbbreviation(t *testing.T) {
	result, suites, _ := runJUnitReportWithIdentity(t, "option-component", "explicit-repo")
	if result.ExitCode != 0 {
		t.Fatalf("expected fake gotestsum to succeed, got %d", result.ExitCode)
	}
	properties := map[string]string{}
	for _, property := range suites.Properties {
		properties[property.Name] = property.Value
	}
	if properties["repo"] != "explicit-repo" {
		t.Fatalf("expected explicit repo abbreviation, got %q", properties["repo"])
	}
}

func assertJUnitSemantics(t *testing.T, suites junitSuites, rhoai bool) {
	t.Helper()
	assertJUnitRootCounters(t, suites)
	if rhoai {
		assertRhoAIJUnitSemantics(t, suites)
		return
	}
	assertVerbatimJUnitSemantics(t, suites)
}

func assertJUnitRootCounters(t *testing.T, suites junitSuites) {
	t.Helper()
	if suites.Tests != 4 || suites.Failures != 1 || suites.Skipped != 1 || suites.Time != 4 {
		t.Fatalf("unexpected root counters: %+v", suites)
	}
}

func assertRhoAIJUnitSemantics(t *testing.T, suites junitSuites) {
	t.Helper()
	if suites.Name != "mcp-lifecycle-operator" || len(suites.Suites) != 1 || suites.Suites[0].Name != "Mcp Lifecycle Operator" {
		t.Fatalf("unexpected rhoai root/suite: %+v", suites)
	}
	suite := suites.Suites[0]
	if suite.Tests != 4 || suite.Failures != 1 || suite.Skipped != 1 || suite.Time != 4 || len(suite.Cases) != 4 {
		t.Fatalf("unexpected rhoai suite counters: %+v", suite)
	}
	for _, caseResult := range suite.Cases {
		if caseResult.Classname != "Mcplo" || caseResult.Time <= 0 {
			t.Errorf("case identity/timing not rewritten: %+v", caseResult)
		}
	}
	assertRhoAICaseNames(t, suite.Cases)
	if suite.Cases[1].Failure == nil || suite.Cases[3].Skipped == nil {
		t.Errorf("failure/skipped semantics were not preserved: %+v", suite.Cases)
	}
	if suite.Cases[0].Stdout != "e2e stdout" || suite.Cases[0].Stderr != "e2e stderr" {
		t.Errorf("stdout/stderr semantics were not preserved: %+v", suite.Cases[0])
	}
}

func assertVerbatimJUnitSemantics(t *testing.T, suites junitSuites) {
	t.Helper()
	if len(suites.Suites) != 3 {
		t.Fatalf("expected three package suites, got %d", len(suites.Suites))
	}
	for _, suite := range suites.Suites {
		assertVerbatimJUnitSuite(t, suite)
	}
	if suites.Suites[1].Cases[0].Failure == nil || suites.Suites[2].Cases[1].Skipped == nil {
		t.Errorf("failure/skipped semantics were not preserved: %+v", suites.Suites)
	}
	if suites.Suites[0].Cases[0].Stdout != "e2e stdout" || suites.Suites[0].Cases[0].Stderr != "e2e stderr" {
		t.Errorf("stdout/stderr semantics were not preserved: %+v", suites.Suites[0].Cases[0])
	}
}

func assertVerbatimJUnitSuite(t *testing.T, suite junitSuite) {
	t.Helper()
	if len(suite.Cases) != suite.Tests {
		t.Fatalf("unexpected suite: %+v", suite)
	}
	for _, caseResult := range suite.Cases {
		if !strings.HasPrefix(caseResult.Name, suite.Name+"/") || caseResult.Classname != "Mcplo" {
			t.Errorf("case identity not rewritten: %+v", caseResult)
		}
		if caseResult.Time <= 0 {
			t.Errorf("case timing was not preserved: %+v", caseResult)
		}
	}
}

func assertRhoAICaseNames(t *testing.T, cases []junitCase) {
	t.Helper()
	prefixes := []string{"tests/e2e/", "tests/upgrade/", "tests/integration/mcpgateway/", "tests/integration/mcpgateway/"}
	for index, prefix := range prefixes {
		if !strings.HasPrefix(cases[index].Name, prefix) {
			t.Errorf("logical package paths were not preserved: %+v", cases)
		}
	}
}

func TestJUnitReportDefaultsToRhoAI(t *testing.T) {
	result, suites, _ := runJUnitReportWithModes(t, "", "mcplo")
	if result.ExitCode != 0 {
		t.Fatalf("expected fake gotestsum to succeed, got %d", result.ExitCode)
	}
	assertJUnitSemantics(t, suites, true)
}

func TestJUnitReportRhoAIExplicit(t *testing.T) {
	result, suites, _ := runJUnitReportWithModes(t, "rhoai", "mcplo")
	if result.ExitCode != 0 {
		t.Fatalf("expected fake gotestsum to succeed, got %d", result.ExitCode)
	}
	assertJUnitSemantics(t, suites, true)
}

func TestJUnitReportVerbatim(t *testing.T) {
	result, suites, _ := runJUnitReportWithModes(t, "verbatim", "mcplo")
	if result.ExitCode != 0 {
		t.Fatalf("expected fake gotestsum to succeed, got %d", result.ExitCode)
	}
	if suites.Name != "mcp-lifecycle-operator" || len(suites.Suites) != 3 {
		t.Fatalf("expected component root with separate package suites: %+v", suites)
	}
	if len(suites.Properties) != 2 {
		t.Fatalf("expected verbatim properties, got %+v", suites.Properties)
	}
	properties := map[string]string{}
	for _, property := range suites.Properties {
		properties[property.Name] = property.Value
	}
	if properties["verbatim"] != "true" || properties["repo"] != "mcplo" {
		t.Fatalf("unexpected verbatim properties: %+v", properties)
	}
	assertJUnitSemantics(t, suites, false)
}

func TestJUnitReportRejectsUnsupportedMode(t *testing.T) {
	result, _, invocation := runJUnitReportWithModes(t, "unsupported", "mcplo")
	if result.ExitCode == 0 {
		t.Fatal("expected unsupported report mode to fail")
	}
	if invocation != "" {
		t.Fatalf("gotestsum was invoked for unsupported mode: %q", invocation)
	}
}

func TestJUnitReportDefaultsRepoAbbr(t *testing.T) {
	result, suites, _ := runJUnitReportWithModes(t, "rhoai", "")
	if result.ExitCode != 0 {
		t.Fatalf("expected default repoabbr to succeed, got %d", result.ExitCode)
	}
	assertJUnitSemantics(t, suites, true)
}

func TestJUnitReportEnvironmentOverridesReportMode(t *testing.T) {
	t.Setenv("E2E_JUNIT_REPORT_MODE", "rhoai")
	result, suites, _ := runJUnitReportWithModes(t, "verbatim", "mcplo")
	if result.ExitCode != 0 {
		t.Fatalf("expected fake gotestsum to succeed, got %d", result.ExitCode)
	}
	assertJUnitSemantics(t, suites, true)
}

func TestRunReturnsFailureWhenGotestsumSucceedsWithoutJUnitReport(t *testing.T) {
	result, logs := runGotestsumWithoutJUnit(t, 0)

	if result.ExitCode == 0 {
		t.Fatalf("expected missing JUnit report to fail, got %d", result.ExitCode)
	}
	if !strings.Contains(logs, "gotestsum succeeded but JUnit report is missing") {
		t.Fatalf("expected missing-report log, got %q", logs)
	}
}

func TestRunPreservesGotestsumFailureWithoutJUnitReport(t *testing.T) {
	result, _ := runGotestsumWithoutJUnit(t, 23)

	if result.ExitCode != 23 {
		t.Fatalf("expected gotestsum exit code 23, got %d", result.ExitCode)
	}
}

func TestRunPreservesGotestsumFailureWhenJUnitRewriteFails(t *testing.T) {
	tmpDir := t.TempDir()
	gotestsumBin := filepath.Join(tmpDir, "gotestsum")
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--junitfile" ]; then junit="$2"; shift 2; continue; fi
  shift
done
mkdir -p "$(dirname "$junit")"
printf '%s' '<testsuites>' > "$junit"
exit 23
`
	if err := os.WriteFile(gotestsumBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := runner.New()
	r.Stdout = &bytes.Buffer{}
	r.Stderr = &bytes.Buffer{}
	r.GotestsumBin = gotestsumBin
	r.ArtifactsDir = tmpDir
	r.ResultsDir = "results"
	r.Packages = []runner.TestPackage{{Name: "e2e", Binary: "/tmp/e2e-tests"}}
	result := r.Run(nil)

	if result.ExitCode != 23 {
		t.Fatalf("expected gotestsum exit code 23, got %d", result.ExitCode)
	}
}

func runGotestsumWithoutJUnit(t *testing.T, exitCode int) (runner.Result, string) {
	t.Helper()
	tmpDir := t.TempDir()
	gotestsumBin := filepath.Join(tmpDir, "gotestsum")
	script := fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode)
	if err := os.WriteFile(gotestsumBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	r := runner.New()
	r.Stdout = &bytes.Buffer{}
	r.Stderr = &stderr
	r.GotestsumBin = gotestsumBin
	r.ArtifactsDir = tmpDir
	r.ResultsDir = "results"
	r.Packages = []runner.TestPackage{{Name: "e2e", Binary: "/tmp/e2e-tests"}}
	return r.Run(nil), stderr.String()
}

func TestRunBuildsCorrectCommand(t *testing.T) {
	tmpDir := t.TempDir()

	gotestsumBin := filepath.Join(tmpDir, "gotestsum")
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" > %q
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--junitfile" ]; then junit="$2"; shift 2; continue; fi
  shift
done
mkdir -p "$(dirname "$junit")"
printf '%%s' '<testsuites name="gotestsum" tests="0" failures="0" skipped="0" time="0"></testsuites>' > "$junit"
`, filepath.Join(tmpDir, "gotestsum.args"))
	if err := os.WriteFile(gotestsumBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	artifactsDir := filepath.Join(tmpDir, "artifacts")

	packages := []runner.TestPackage{
		{Name: "mypackage", Binary: "/tmp/e2e-tests"},
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	r := runner.New()
	r.Stdout = &stdoutBuf
	r.Stderr = &stderrBuf
	r.Packages = packages
	r.ArtifactsDir = artifactsDir
	r.ResultsDir = "results"
	r.GotestsumBin = gotestsumBin
	r.Component = "mcp-lifecycle-operator"
	r.RepoAbbr = "mcplo"
	r.Verbosity = "standard-verbose"
	r.TestCount = "3"
	result := r.Run([]string{"-test.timeout=5m", "-test.run=TestFoo"})

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
		"--junitfile-project-name mcp-lifecycle-operator",
		"--jsonfile " + filepath.Join(resultsDir, "log.jsonl"),
		"--format standard-verbose",
		"-- " + os.Args[0] + " exec",
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
	r := runner.New()
	r.Stdout = &stdoutBuf
	r.Stderr = &stderrBuf
	r.Packages = []runner.TestPackage{
		{Name: "e2e", Binary: binary1},
		{Name: "integration", Binary: binary2},
	}
	r.Test2JSONBin = filepath.Join(tmpDir, "fallback-test2json")
	r.TestCount = "fallback-count"
	result := r.Run([]string{"exec", "-test.run=TestFoo"})

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

func TestExecReturnsFailureWhenTest2JSONIsTerminated(t *testing.T) {
	// Given
	tmpDir := t.TempDir()
	test2jsonBin := filepath.Join(tmpDir, "test2json")
	if err := os.WriteFile(test2jsonBin, []byte("#!/bin/sh\nkill -TERM $$\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := runner.New()
	r.Stdout = &bytes.Buffer{}
	r.Stderr = &bytes.Buffer{}
	r.Test2JSONBin = test2jsonBin
	r.Packages = []runner.TestPackage{{Name: "e2e", Binary: "/tmp/e2e-tests"}}

	// When
	result := r.Run([]string{"exec"})

	// Then
	if result.ExitCode != 1 {
		t.Fatalf("expected signal termination to return exit code 1, got %d", result.ExitCode)
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
