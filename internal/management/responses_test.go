package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternalErrorWithoutLoggerHidesCause(t *testing.T) {
	t.Parallel()

	h := &Handlers{}
	rec := httptest.NewRecorder()
	h.internalError(rec, httptest.NewRequest(http.MethodGet, "/api/management/users", nil), "failed to list users", errors.New("database connection lost"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	var response errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Code != errorCodeInternal || response.Error.Message != "failed to list users" {
		t.Fatalf("unexpected public error: %+v", response.Error)
	}
}
