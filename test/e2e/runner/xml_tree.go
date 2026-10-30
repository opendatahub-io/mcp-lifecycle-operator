package runner

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
)

type xmlNode struct {
	token   xml.Token
	element *xmlElement
}

type xmlElement struct {
	start xml.StartElement
	nodes []xmlNode
}

type xmlDocument struct {
	leading  []xml.Token
	root     *xmlElement
	trailing []xml.Token
}

func parseXMLDocument(data []byte) (*xmlDocument, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	document := &xmlDocument{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if document.root == nil {
				return nil, fmt.Errorf("missing XML root element")
			}
			return document, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read XML token: %w", err)
		}
		if start, ok := token.(xml.StartElement); ok {
			if document.root != nil {
				return nil, fmt.Errorf("multiple XML root elements")
			}
			root, err := parseXMLElement(decoder, start)
			if err != nil {
				return nil, err
			}
			document.root = root
			continue
		}
		if document.root == nil {
			document.leading = append(document.leading, xml.CopyToken(token))
		} else {
			document.trailing = append(document.trailing, xml.CopyToken(token))
		}
	}
}

func parseXMLElement(decoder *xml.Decoder, start xml.StartElement) (*xmlElement, error) {
	element := &xmlElement{start: xml.CopyToken(start).(xml.StartElement)}
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("read XML element %s: %w", start.Name.Local, err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			child, err := parseXMLElement(decoder, value)
			if err != nil {
				return nil, err
			}
			element.nodes = append(element.nodes, xmlNode{element: child})
		case xml.EndElement:
			return element, nil
		default:
			element.nodes = append(element.nodes, xmlNode{token: xml.CopyToken(token)})
		}
	}
}

func marshalXMLDocument(document *xmlDocument) ([]byte, error) {
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	for _, token := range document.leading {
		if err := encoder.EncodeToken(token); err != nil {
			return nil, fmt.Errorf("write XML header: %w", err)
		}
	}
	if err := marshalXMLElement(encoder, document.root); err != nil {
		return nil, err
	}
	for _, token := range document.trailing {
		if err := encoder.EncodeToken(token); err != nil {
			return nil, fmt.Errorf("write XML trailer: %w", err)
		}
	}
	if err := encoder.Flush(); err != nil {
		return nil, fmt.Errorf("flush XML: %w", err)
	}
	return output.Bytes(), nil
}

func marshalXMLElement(encoder *xml.Encoder, element *xmlElement) error {
	if err := encoder.EncodeToken(element.start); err != nil {
		return fmt.Errorf("write XML element %s: %w", element.start.Name.Local, err)
	}
	for _, node := range element.nodes {
		if node.element != nil {
			if err := marshalXMLElement(encoder, node.element); err != nil {
				return err
			}
			continue
		}
		if err := encoder.EncodeToken(node.token); err != nil {
			return fmt.Errorf("write XML token: %w", err)
		}
	}
	if err := encoder.EncodeToken(element.start.End()); err != nil {
		return fmt.Errorf("close XML element %s: %w", element.start.Name.Local, err)
	}
	return nil
}

func (element *xmlElement) attribute(name string) (string, bool) {
	for _, attribute := range element.start.Attr {
		if attribute.Name.Space == "" && attribute.Name.Local == name {
			return attribute.Value, true
		}
	}
	return "", false
}

func (element *xmlElement) setAttribute(name, value string) {
	for index := range element.start.Attr {
		attribute := &element.start.Attr[index]
		if attribute.Name.Space == "" && attribute.Name.Local == name {
			attribute.Value = value
			return
		}
	}
	element.start.Attr = append(element.start.Attr, xml.Attr{Name: xml.Name{Local: name}, Value: value})
}

func startElement(name string) xml.StartElement {
	return xml.StartElement{Name: xml.Name{Local: name}}
}
