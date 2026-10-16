package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadBodyEnforcesExactLimit(t *testing.T) {
	for _, unknownLength := range []bool{false, true} {
		for _, body := range []string{"1234", "12345"} {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			if unknownLength {
				request.ContentLength = -1
			}
			got, err := ReadBody(httptest.NewRecorder(), request, 4)
			if len(body) == 4 {
				if err != nil || string(got) != body {
					t.Fatalf("exact-limit body: %q, %v", got, err)
				}
			} else {
				var tooLarge *http.MaxBytesError
				if !errors.As(err, &tooLarge) {
					t.Fatalf("oversized body: %v, want MaxBytesError", err)
				}
			}
		}
	}
}

func TestDecodeJSONBodyFramingAndOptions(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		options JSONBodyOptions
		wantErr bool
	}{
		{"single object", `{"name":"ok"}`, JSONBodyOptions{}, false},
		{"trailing whitespace", "{} \r\n\t", JSONBodyOptions{}, false},
		{"second object", `{} {}`, JSONBodyOptions{}, true},
		{"trailing garbage", `{}invalid`, JSONBodyOptions{}, true},
		{"required empty", ``, JSONBodyOptions{}, true},
		{"optional empty", ``, JSONBodyOptions{AllowEmpty: true}, false},
		{"strict unknown field", `{"other":true}`, JSONBodyOptions{RejectUnknownFields: true}, true},
		{"compatible unknown field", `{"other":true}`, JSONBodyOptions{}, false},
		{"oversized trailing whitespace", `{}` + strings.Repeat(" ", MaxJSONBodyBytes), JSONBodyOptions{}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var destination struct{ Name string }
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			request.ContentLength = -1
			err := DecodeJSONBody(httptest.NewRecorder(), request, &destination, test.options)
			if (err != nil) != test.wantErr {
				t.Fatalf("decode error = %v, wantErr=%t", err, test.wantErr)
			}
		})
	}
}
