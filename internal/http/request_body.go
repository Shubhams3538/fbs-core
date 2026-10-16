package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/i-got-this-faa/fbs/internal/auth"
)

// MaxJSONBodyBytes bounds management and setup control requests.
const MaxJSONBodyBytes = 1 << 20

// ErrPayloadHashMismatch rejects a control body before its handler can mutate state.
var ErrPayloadHashMismatch = errors.New("request body does not match signed payload hash")

// JSONBodyOptions preserves the empty-body and field policies of each endpoint.
type JSONBodyOptions struct {
	AllowEmpty          bool
	RejectUnknownFields bool
}

// ReadBody bounds control bodies and verifies any authenticated SigV4 payload hash.
func ReadBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	if r.ContentLength > limit {
		return nil, &http.MaxBytesError{Limit: limit}
	}
	var payload []byte
	if r.Body != nil {
		var err error
		payload, err = io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			return nil, err
		}
	}
	principal, _ := auth.PrincipalFromContext(r.Context())
	if expected := principal.SignedPayloadHash; expected != "" && expected != "UNSIGNED-PAYLOAD" {
		actual := sha256.Sum256(payload)
		if !strings.EqualFold(expected, hex.EncodeToString(actual[:])) {
			return nil, ErrPayloadHashMismatch
		}
	}
	return payload, nil
}

// DecodeJSONBody accepts one bounded JSON value and preserves optional empty bodies.
func DecodeJSONBody(w http.ResponseWriter, r *http.Request, destination any, options JSONBodyOptions) error {
	payload, err := ReadBody(w, r, MaxJSONBodyBytes)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if options.RejectUnknownFields {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(destination); err != nil {
		if options.AllowEmpty && errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("request body must contain one JSON value")
	}
	return nil
}
