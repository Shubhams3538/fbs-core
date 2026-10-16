package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// MaxJSONBodyBytes bounds management and setup control requests.
const MaxJSONBodyBytes = 1 << 20

// JSONBodyOptions preserves the empty-body and field policies of each endpoint.
type JSONBodyOptions struct {
	AllowEmpty          bool
	RejectUnknownFields bool
}

// ReadBody rejects oversized bodies rather than accepting a truncated prefix.
func ReadBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	if r.ContentLength > limit {
		return nil, &http.MaxBytesError{Limit: limit}
	}
	return io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
}

// DecodeJSONBody accepts one bounded JSON value and preserves optional empty bodies.
func DecodeJSONBody(w http.ResponseWriter, r *http.Request, destination any, options JSONBodyOptions) error {
	if r.Body == nil {
		if options.AllowEmpty {
			return nil
		}
		return io.EOF
	}
	defer r.Body.Close()
	if r.ContentLength > MaxJSONBodyBytes {
		return &http.MaxBytesError{Limit: MaxJSONBodyBytes}
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxJSONBodyBytes))
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
