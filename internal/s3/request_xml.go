package s3

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

var errInvalidXMLDocument = errors.New("request body must contain one XML document")

// decodeXMLRequest requires one root element, with only XML miscellany outside
// it. Decode/Unmarshal alone stop at that element and can ignore a malformed tail.
func decodeXMLRequest(payload []byte, destination any) error {
	payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	first := true
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if start, ok := token.(xml.StartElement); ok {
			if err := decoder.DecodeElement(destination, &start); err != nil {
				return err
			}
			return finishXMLRequest(decoder)
		}
		if !isXMLMisc(token, true) {
			return errInvalidXMLDocument
		}
		if instruction, ok := token.(xml.ProcInst); ok && strings.EqualFold(instruction.Target, "xml") {
			if !first || instruction.Target != "xml" {
				return errInvalidXMLDocument
			}
		}
		first = false
	}
}

func finishXMLRequest(decoder *xml.Decoder) error {
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if !isXMLMisc(token, false) {
			return errInvalidXMLDocument
		}
	}
}

func isXMLMisc(token xml.Token, prolog bool) bool {
	switch token := token.(type) {
	case xml.CharData:
		for _, character := range token {
			switch character {
			case ' ', '\t', '\r', '\n':
			default:
				return false
			}
		}
		return true
	case xml.Comment:
		return true
	case xml.ProcInst:
		return prolog || !strings.EqualFold(token.Target, "xml")
	case xml.Directive:
		return prolog
	default:
		return false
	}
}
