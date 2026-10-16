package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/i-got-this-faa/fbs/internal/auth"
	"github.com/i-got-this-faa/fbs/internal/management"
)

type errorAuthenticator struct {
	err error
}

func (authenticator errorAuthenticator) Authenticate(*http.Request) (auth.Principal, error) {
	return auth.Principal{}, authenticator.err
}

func TestAuthErrorResponses(t *testing.T) {
	t.Parallel()

	responders := []struct {
		name        string
		respond     func(http.ResponseWriter, *http.Request, error)
		contentType string
	}{
		{"debug JSON", writeJSONAuthError, "application/json; charset=utf-8"},
		{"S3", writeS3AuthError, "application/xml"},
		{"management", management.WriteAuthError, "application/json; charset=utf-8"},
	}
	for _, responder := range responders {
		t.Run(responder.name, func(t *testing.T) {
			for _, test := range []struct {
				name      string
				err       error
				status    int
				challenge string
			}{
				{"missing", auth.ErrMissingAuth, http.StatusUnauthorized, `Bearer realm="fbs"`},
				{"unsupported", auth.ErrUnsupportedScheme, http.StatusUnauthorized, ""},
				{"malformed", auth.ErrMalformedToken, http.StatusUnauthorized, ""},
				{"inactive", auth.ErrInactiveUser, http.StatusForbidden, ""},
				{"forbidden", auth.ErrForbidden, http.StatusForbidden, ""},
				{"internal", auth.ErrInternal, http.StatusInternalServerError, ""},
				{"unknown", errors.New("unexpected auth failure"), http.StatusUnauthorized, ""},
			} {
				t.Run(test.name, func(t *testing.T) {
					const privateCause = "database password=do-not-expose"
					request := httptest.NewRequest(http.MethodGet, "/protected", nil)
					recorder := httptest.NewRecorder()
					authenticator := &auth.ChainAuthenticator{Authenticators: []auth.Authenticator{
						errorAuthenticator{err: fmt.Errorf("%w: %s", test.err, privateCause)},
					}}
					handler := auth.RequireAuthentication(authenticator, responder.respond)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
						t.Fatal("authentication failure reached the protected handler")
					}))
					handler.ServeHTTP(recorder, request)
					if recorder.Code != test.status {
						t.Fatalf("status = %d, want %d", recorder.Code, test.status)
					}
					if got := recorder.Header().Get("WWW-Authenticate"); got != test.challenge {
						t.Errorf("WWW-Authenticate = %q, want %q", got, test.challenge)
					}
					if got := recorder.Header().Get("Content-Type"); got != responder.contentType {
						t.Errorf("Content-Type = %q, want %q", got, responder.contentType)
					}
					if strings.Contains(recorder.Body.String(), privateCause) {
						t.Fatal("response exposes the private authentication cause")
					}
					assertAuthErrorBody(t, recorder.Body.Bytes(), responder.name)
				})
			}
		})
	}
}

func assertAuthErrorBody(t *testing.T, body []byte, responder string) {
	t.Helper()
	if responder == "S3" {
		var response struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		if err := xml.Unmarshal(body, &response); err != nil {
			t.Fatalf("decode S3 error: %v", err)
		}
		if response.Code == "" || response.Message == "" {
			t.Fatalf("missing S3 error code or message: %s", body)
		}
		return
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode JSON error: %v", err)
	}
	if len(response["error"]) == 0 {
		t.Fatalf("missing JSON error: %s", body)
	}
}
