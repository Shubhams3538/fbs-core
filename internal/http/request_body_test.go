package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/i-got-this-faa/fbs/internal/auth"
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

func TestSignedControlBodyIntegrity(t *testing.T) {
	for _, test := range []struct {
		name string
		body io.ReadCloser
		hash string
		want error
	}{
		{"nil empty digest", nil, auth.EmptyStringHash, nil},
		{"nil nonempty digest", nil, strings.Repeat("0", 64), ErrPayloadHashMismatch},
		{"NoBody empty digest", http.NoBody, auth.EmptyStringHash, nil},
		{"NoBody nonempty digest", http.NoBody, strings.Repeat("0", 64), ErrPayloadHashMismatch},
		{"empty unsigned", nil, "UNSIGNED-PAYLOAD", nil},
		{"malformed digest", nil, "not-a-sha256-digest", ErrPayloadHashMismatch},
		{"nonempty body empty digest", io.NopCloser(strings.NewReader("{}")), auth.EmptyStringHash, ErrPayloadHashMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			request.Body = test.body
			request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{SignedPayloadHash: test.hash}))
			var destination map[string]string
			err := DecodeJSONBody(httptest.NewRecorder(), request, &destination, JSONBodyOptions{AllowEmpty: true})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
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
