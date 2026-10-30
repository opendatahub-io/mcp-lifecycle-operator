package runner

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestTransformJUnitReportMergesSuitePropertiesBeforeTestcases(t *testing.T) {
	input := `<testsuites><testsuite name="first"><properties><property name="one" value="1"/></properties><testcase name="first-case"/></testsuite><testsuite name="second"><properties><property name="two" value="2"/></properties><testcase name="second-case"/></testsuite></testsuites>`

	output, err := transformJUnitReport([]byte(input), junitReportConfig{mode: junitReportModeRhoAI, component: "component"})
	if err != nil {
		t.Fatal(err)
	}

	var report struct {
		Suite struct {
			Properties []struct {
				Name  string `xml:"name,attr"`
				Value string `xml:"value,attr"`
			} `xml:"properties>property"`
			Cases []struct {
				Name string `xml:"name,attr"`
			} `xml:"testcase"`
		} `xml:"testsuite"`
	}
	if err := xml.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}

	if len(report.Suite.Properties) != 2 {
		t.Fatalf("expected both suite properties, got %+v", report.Suite.Properties)
	}
	if report.Suite.Properties[0].Name != "one" || report.Suite.Properties[1].Name != "two" {
		t.Fatalf("properties were not retained in source order: %+v", report.Suite.Properties)
	}
	if len(report.Suite.Cases) != 2 || report.Suite.Cases[0].Name != "first/first-case" || report.Suite.Cases[1].Name != "second/second-case" {
		t.Fatalf("testcase order was not preserved: %+v", report.Suite.Cases)
	}
	if strings.Count(string(output), "<properties>") != 1 || strings.Index(string(output), "<properties>") > strings.Index(string(output), "<testcase") {
		t.Fatalf("expected one properties block before testcases: %s", output)
	}
}
