package s3

import (
	"encoding/xml"
	"testing"
)

func TestDecodeXMLRequestFraming(t *testing.T) {
	for _, test := range []struct {
		name, document string
		valid          bool
	}{
		{"root", `<Request><Value>ok</Value></Request>`, true},
		{"XML whitespace", " \r\n\t<Request/> \r\n\t", true},
		{"declaration", `<?xml version="1.0" encoding="UTF-8"?><Request/>`, true},
		{"BOM", "\xef\xbb\xbf<Request/>", true},
		{"comments", `<!--before--><Request/><!--after-->`, true},
		{"processing instructions", `<?before value?><Request/><?after value?>`, true},
		{"namespace", `<Request xmlns="urn:example"><Value>ok</Value></Request>`, true},
		{"declaration comment", `<?xml version="1.0"?><!--header--><Request/>`, true},
		{"second root", `<Request/><Request/>`, false},
		{"trailing text", `<Request/>garbage`, false},
		{"leading text", `garbage<Request/>`, false},
		{"broken comment", `<Request/><!--`, false},
		{"unmatched tail", `<Request/></Other>`, false},
		{"declaration after root", `<Request/><?xml version="1.0"?>`, false},
		{"duplicate declaration", `<?xml version="1.0"?><?xml version="1.0"?><Request/>`, false},
		{"declaration after comment", `<!--header--><?xml version="1.0"?><Request/>`, false},
		{"trailing directive", `<Request/><!DOCTYPE Request>`, false},
		{"wrong root", `<Other/>`, false},
		{"empty", "", false},
		{"whitespace only", " \t\n", false},
		{"truncated", `<Request>`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request struct {
				XMLName xml.Name `xml:"Request"`
				Value   string   `xml:"Value"`
			}
			err := decodeXMLRequest([]byte(test.document), &request)
			if (err == nil) != test.valid {
				t.Fatalf("error=%v, want valid=%t", err, test.valid)
			}
		})
	}
}
