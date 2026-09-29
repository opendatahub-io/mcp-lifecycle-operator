package runner

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
)

type junitReportMode string

const (
	junitReportModeRhoAI    junitReportMode = "rhoai"
	junitReportModeVerbatim junitReportMode = "verbatim"
	testSuiteElement                        = "testsuite"
)

type junitReportConfig struct {
	mode      junitReportMode
	component string
	repoAbbr  string
}

func parseJUnitReportMode(value string) (junitReportMode, error) {
	switch junitReportMode(value) {
	case "", junitReportModeRhoAI:
		return junitReportModeRhoAI, nil
	case junitReportModeVerbatim:
		return junitReportModeVerbatim, nil
	default:
		return "", fmt.Errorf("%q: expected rhoai or verbatim", value)
	}
}

func rewriteJUnitReport(path string, config junitReportConfig) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	rewritten, err := transformJUnitReport(data, config)
	if err != nil {
		return fmt.Errorf("transform %s: %w", path, err)
	}
	if err := os.WriteFile(path, rewritten, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func transformJUnitReport(data []byte, config junitReportConfig) ([]byte, error) {
	document, err := parseXMLDocument(data)
	if err != nil {
		return nil, err
	}
	if document.root.start.Name.Local != "testsuites" {
		return nil, fmt.Errorf("expected testsuites root, got %s", document.root.start.Name.Local)
	}

	document.root.setAttribute("name", config.component)
	switch config.mode {
	case junitReportModeRhoAI:
		if err := flattenSuites(document.root, config); err != nil {
			return nil, err
		}
	case junitReportModeVerbatim:
		rewriteSuites(document.root, config.repoAbbr)
		setRootProperty(document.root, "verbatim", "true")
		setRootProperty(document.root, "repo", config.repoAbbr)
	default:
		return nil, fmt.Errorf("unsupported report mode %q", config.mode)
	}

	return marshalXMLDocument(document)
}

func flattenSuites(root *xmlElement, config junitReportConfig) error {
	filtered := make([]xmlNode, 0, len(root.nodes))
	var suites []*xmlElement
	insertAt := -1
	for _, node := range root.nodes {
		if node.element != nil && node.element.start.Name.Local == testSuiteElement {
			if insertAt == -1 {
				insertAt = len(filtered)
			}
			rewriteSuite(node.element, config.repoAbbr)
			suites = append(suites, node.element)
			continue
		}
		filtered = append(filtered, node)
	}

	aggregate := &xmlElement{start: startElement(testSuiteElement)}
	if len(suites) > 0 {
		aggregate.start = xml.CopyToken(suites[0].start).(xml.StartElement)
		for _, suite := range suites {
			aggregate.nodes = append(aggregate.nodes, suite.nodes...)
		}
		if err := aggregateCounters(aggregate, suites); err != nil {
			return err
		}
	} else {
		aggregate.start.Attr = append(aggregate.start.Attr, root.start.Attr...)
	}
	aggregate.setAttribute("name", titleWords(config.component))
	if insertAt == -1 {
		filtered = append(filtered, xmlNode{element: aggregate})
	} else {
		filtered = append(filtered, xmlNode{})
		copy(filtered[insertAt+1:], filtered[insertAt:])
		filtered[insertAt] = xmlNode{element: aggregate}
	}
	root.nodes = filtered
	return nil
}

func aggregateCounters(aggregate *xmlElement, suites []*xmlElement) error {
	for _, name := range []string{"tests", "failures", "errors", "skipped", "disabled", "assertions"} {
		var total int64
		found := false
		for _, suite := range suites {
			value, ok := suite.attribute(name)
			if !ok {
				continue
			}
			count, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return fmt.Errorf("parse testsuite %s=%q: %w", name, value, err)
			}
			total += count
			found = true
		}
		if found {
			aggregate.setAttribute(name, strconv.FormatInt(total, 10))
		}
	}

	var total float64
	found := false
	for _, suite := range suites {
		value, ok := suite.attribute("time")
		if !ok {
			continue
		}
		seconds, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("parse testsuite time=%q: %w", value, err)
		}
		total += seconds
		found = true
	}
	if found {
		aggregate.setAttribute("time", strconv.FormatFloat(total, 'f', -1, 64))
	}
	return nil
}

func rewriteSuites(root *xmlElement, repoAbbr string) {
	for _, node := range root.nodes {
		if node.element != nil && node.element.start.Name.Local == testSuiteElement {
			rewriteSuite(node.element, repoAbbr)
		}
	}
}

func rewriteSuite(suite *xmlElement, repoAbbr string) {
	suiteName, _ := suite.attribute("name")
	for _, node := range suite.nodes {
		if node.element == nil {
			continue
		}
		switch node.element.start.Name.Local {
		case "testcase":
			name, _ := node.element.attribute("name")
			node.element.setAttribute("name", packageCaseName(suiteName, name))
			node.element.setAttribute("classname", titleCase(repoAbbr))
		case testSuiteElement:
			rewriteSuite(node.element, repoAbbr)
		}
	}
}

func setRootProperty(root *xmlElement, name, value string) {
	var properties *xmlElement
	found := false
	for _, node := range root.nodes {
		if node.element == nil || node.element.start.Name.Local != "properties" {
			continue
		}
		if properties == nil {
			properties = node.element
		}
		for _, property := range node.element.nodes {
			if property.element == nil || property.element.start.Name.Local != "property" {
				continue
			}
			propertyName, ok := property.element.attribute("name")
			if ok && propertyName == name {
				property.element.setAttribute("value", value)
				found = true
			}
		}
	}
	if properties == nil {
		properties = &xmlElement{start: startElement("properties")}
		root.nodes = append([]xmlNode{{element: properties}}, root.nodes...)
	}
	if found {
		return
	}
	property := &xmlElement{start: startElement("property")}
	property.setAttribute("name", name)
	property.setAttribute("value", value)
	properties.nodes = append(properties.nodes, xmlNode{element: property})
}

func packageCaseName(suiteName, caseName string) string {
	prefix := suiteName + "/"
	if strings.HasPrefix(caseName, prefix) {
		return caseName
	}
	return prefix + caseName
}

func titleWords(value string) string {
	words := strings.Fields(strings.ReplaceAll(value, "-", " "))
	for index := range words {
		words[index] = titleCase(words[index])
	}
	return strings.Join(words, " ")
}

func titleCase(value string) string {
	runes := []rune(strings.ToLower(value))
	if len(runes) > 0 {
		runes[0] = unicode.ToUpper(runes[0])
	}
	return string(runes)
}
