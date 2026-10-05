package runner

import (
	"os"
	"testing"
)

func TestInitializeFillsDefaults(t *testing.T) {
	r := &Runner{}

	if err := r.initialize(); err != nil {
		t.Fatalf("initialize() error = %v", err)
	}

	if r.Stdout != os.Stdout || r.Stderr != os.Stderr || r.selfPath != os.Args[0] ||
		r.ArtifactsDir != defaultArtifactsDir || r.ResultsDir != defaultResultsDir ||
		r.GotestsumBin != defaultGotestsumBin || r.Verbosity != "testname" ||
		r.Test2JSONBin != defaultTest2JSONBin || r.TestCount != "1" ||
		r.Component != defaultComponent || r.RepoAbbr != "mcplo" || r.ReportMode != "rhoai" {
		t.Fatalf("unexpected defaults: %+v", r)
	}
}

func TestInitializePreservesCallerFields(t *testing.T) {
	r := &Runner{
		ArtifactsDir: "artifacts",
		ResultsDir:   "results",
		GotestsumBin: "gotestsum-custom",
		Verbosity:    "verbose",
		Test2JSONBin: "test2json-custom",
		TestCount:    "3",
		Component:    "custom-component",
		RepoAbbr:     "custom-repo",
		ReportMode:   "verbatim",
	}

	if err := r.initialize(); err != nil {
		t.Fatalf("initialize() error = %v", err)
	}

	if r.ArtifactsDir != "artifacts" || r.ResultsDir != "results" || r.GotestsumBin != "gotestsum-custom" ||
		r.Verbosity != "verbose" || r.Test2JSONBin != "test2json-custom" || r.TestCount != "3" ||
		r.Component != "custom-component" || r.RepoAbbr != "custom-repo" || r.ReportMode != "verbatim" {
		t.Fatalf("caller fields changed: %+v", r)
	}
}

func TestInitializeEnvironmentOverridesHavePrecedence(t *testing.T) {
	for key, value := range map[string]string{
		"ARTIFACTS":             "env-artifacts",
		"E2E_RESULTS_DIR":       "env-results",
		"E2E_GOTESTSUM_BIN":     "env-gotestsum",
		"E2E_GO_TEST_VERBOSITY": "env-verbosity",
		"E2E_COMPONENT":         "env-component",
		"E2E_REPO_ABBR":         "env-repo",
		"E2E_JUNIT_REPORT_MODE": "verbatim",
		"E2E_TEST2JSON_BIN":     "env-test2json",
		"E2E_COUNT":             "7",
	} {
		t.Setenv(key, value)
	}
	r := &Runner{
		ArtifactsDir: "code-artifacts",
		ResultsDir:   "code-results",
		GotestsumBin: "code-gotestsum",
		Verbosity:    "code-verbosity",
		Test2JSONBin: "code-test2json",
		TestCount:    "2",
		Component:    "code-component",
		RepoAbbr:     "code-repo",
		ReportMode:   "rhoai",
	}

	if err := r.initialize(); err != nil {
		t.Fatalf("initialize() error = %v", err)
	}

	if r.ArtifactsDir != "env-artifacts" || r.ResultsDir != "env-results" || r.GotestsumBin != "env-gotestsum" ||
		r.Verbosity != "env-verbosity" || r.Test2JSONBin != "env-test2json" || r.TestCount != "7" ||
		r.Component != "env-component" || r.RepoAbbr != "env-repo" || r.ReportMode != "verbatim" {
		t.Fatalf("environment did not take precedence: %+v", r)
	}
}

func TestInitializeDerivesRepoAbbrAfterComponentResolution(t *testing.T) {
	t.Setenv("E2E_COMPONENT", "resolved-component")
	r := &Runner{}

	if err := r.initialize(); err != nil {
		t.Fatalf("initialize() error = %v", err)
	}

	if r.Component != "resolved-component" || r.RepoAbbr != "resolvedc" {
		t.Fatalf("component/repo abbreviation = %q/%q", r.Component, r.RepoAbbr)
	}
}

func TestInitializePreservesExplicitRepoAbbr(t *testing.T) {
	r := &Runner{Component: "custom-component", RepoAbbr: "explicit-repo"}

	if err := r.initialize(); err != nil {
		t.Fatalf("initialize() error = %v", err)
	}

	if r.RepoAbbr != "explicit-repo" {
		t.Fatalf("RepoAbbr = %q, want explicit-repo", r.RepoAbbr)
	}
}

func TestInitializeIgnoresOldUnprefixedEnvironmentNames(t *testing.T) {
	t.Setenv("GO_TEST_VERBOSITY", "legacy-verbosity")
	t.Setenv("component", "legacy-component")
	t.Setenv("repoabbr", "legacy-repo")
	r := &Runner{Verbosity: "caller-verbosity", Component: "caller-component"}

	if err := r.initialize(); err != nil {
		t.Fatalf("initialize() error = %v", err)
	}

	if r.Verbosity != "caller-verbosity" || r.Component != "caller-component" || r.RepoAbbr != "callerc" {
		t.Fatalf("legacy environment names were honored: %+v", r)
	}
}

func TestInitializeRejectsUnsupportedReportMode(t *testing.T) {
	r := &Runner{ReportMode: "unsupported"}

	if err := r.initialize(); err == nil {
		t.Fatal("initialize() error = nil, want unsupported report mode error")
	}
}
