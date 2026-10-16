package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/i-got-this-faa/fbs/internal/auth"
	httpapi "github.com/i-got-this-faa/fbs/internal/http"
	"github.com/i-got-this-faa/fbs/internal/iam"
	"github.com/i-got-this-faa/fbs/internal/metadata"
)

func (h *Handlers) ListKeys(w http.ResponseWriter, r *http.Request) {
	users, err := h.Users.List(r.Context())
	if err != nil {
		h.internalError(w, r, "failed to list keys", err)
		return
	}

	keys := make([]keyResponse, 0, len(users))
	for _, user := range users {
		keys = append(keys, keyDTO(user))
	}

	writeJSON(w, http.StatusOK, keysResponse{Keys: keys})
}

func (h *Handlers) CreateKey(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeCreateKeyRequest(w, r)
	if !ok {
		return
	}

	issued, sigv4Creds, user, err := auth.CreateBearerToken(r.Context(), h.Users, req.displayName, req.role)
	if err != nil {
		h.internalError(w, r, "failed to create key", err)
		return
	}

	writeJSON(w, http.StatusCreated, createKeyResponse{
		Key:         keyDTO(*user),
		BearerToken: issued.RawToken,
		SigV4: sigv4Response{
			AccessKeyID: sigv4Creds.AccessKeyID,
			SecretKey:   sigv4Creds.SecretKey,
		},
	})
}

func (h *Handlers) DeleteKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "key not found")
		return
	}

	err := h.Users.Delete(r.Context(), id)
	if errors.Is(err, metadata.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "key not found")
		return
	}
	if err != nil {
		h.internalError(w, r, "failed to delete key", err)
		return
	}

	setNoStoreHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) PatchKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "key not found")
		return
	}

	req, ok := decodePatchKeyRequest(w, r)
	if !ok {
		return
	}

	user, err := h.Users.GetByID(r.Context(), id)
	if errors.Is(err, metadata.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "key not found")
		return
	}
	if err != nil {
		h.internalError(w, r, "failed to load key", err)
		return
	}

	if req.displayName != nil {
		user.DisplayName = *req.displayName
	}
	if req.isActive != nil {
		user.IsActive = *req.isActive
	}

	if err := h.Users.Update(r.Context(), user); errors.Is(err, metadata.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "key not found")
		return
	} else if err != nil {
		h.internalError(w, r, "failed to update key", err)
		return
	}

	updated, err := h.Users.GetByID(r.Context(), id)
	if err != nil {
		h.internalError(w, r, "failed to load updated key", err)
		return
	}

	writeJSON(w, http.StatusOK, keyEnvelopeResponse{Key: keyDTO(*updated)})
}

type createKeyInput struct {
	displayName string
	role        iam.Role
}

func decodeCreateKeyRequest(w http.ResponseWriter, r *http.Request) (createKeyInput, bool) {
	var rawFields map[string]json.RawMessage
	if err := httpapi.DecodeJSONBody(w, r, &rawFields, httpapi.JSONBodyOptions{}); err != nil {
		writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "invalid JSON request body")
		return createKeyInput{}, false
	}

	for field := range rawFields {
		if field != "display_name" && field != "role" {
			writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "invalid JSON request body")
			return createKeyInput{}, false
		}
	}

	displayName, ok := decodeStringField(w, rawFields, "display_name")
	if !ok {
		return createKeyInput{}, false
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "display_name is required")
		return createKeyInput{}, false
	}

	role := iam.RoleMember
	if _, exists := rawFields["role"]; exists {
		decodedRole, ok := decodeStringField(w, rawFields, "role")
		if !ok {
			return createKeyInput{}, false
		}
		parsedRole, err := iam.ParseRole(strings.TrimSpace(decodedRole))
		if err != nil {
			writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, err.Error())
			return createKeyInput{}, false
		}
		role = parsedRole
	}

	return createKeyInput{displayName: displayName, role: role}, true
}

type patchKeyInput struct {
	displayName *string
	isActive    *bool
}

func decodePatchKeyRequest(w http.ResponseWriter, r *http.Request) (patchKeyInput, bool) {
	var rawFields map[string]json.RawMessage
	if err := httpapi.DecodeJSONBody(w, r, &rawFields, httpapi.JSONBodyOptions{}); err != nil {
		writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "invalid JSON request body")
		return patchKeyInput{}, false
	}

	for field := range rawFields {
		if field != "display_name" && field != "is_active" {
			writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "invalid JSON request body")
			return patchKeyInput{}, false
		}
	}
	if len(rawFields) == 0 {
		writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "at least one field is required")
		return patchKeyInput{}, false
	}

	var input patchKeyInput
	if _, exists := rawFields["display_name"]; exists {
		displayName, ok := decodeStringField(w, rawFields, "display_name")
		if !ok {
			return patchKeyInput{}, false
		}
		displayName = strings.TrimSpace(displayName)
		if displayName == "" {
			writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "display_name must not be empty")
			return patchKeyInput{}, false
		}
		input.displayName = &displayName
	}
	if _, exists := rawFields["is_active"]; exists {
		isActive, ok := decodeBoolField(w, rawFields, "is_active")
		if !ok {
			return patchKeyInput{}, false
		}
		input.isActive = &isActive
	}

	return input, true
}
