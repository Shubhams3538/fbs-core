package management

import (
	"encoding/json"
	"net/http"
)

func decodeStringField(w http.ResponseWriter, rawFields map[string]json.RawMessage, field string) (string, bool) {
	rawValue, exists := rawFields[field]
	if !exists {
		return "", true
	}

	var value string
	if err := json.Unmarshal(rawValue, &value); err != nil || string(rawValue) == "null" {
		writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, field+" must be a string")
		return "", false
	}

	return value, true
}

func decodeBoolField(w http.ResponseWriter, rawFields map[string]json.RawMessage, field string) (bool, bool) {
	rawValue, exists := rawFields[field]
	if !exists {
		return false, true
	}

	var value bool
	if err := json.Unmarshal(rawValue, &value); err != nil || string(rawValue) == "null" {
		writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, field+" must be a boolean")
		return false, false
	}

	return value, true
}
