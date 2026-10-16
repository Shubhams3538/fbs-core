package management

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	httpapi "github.com/i-got-this-faa/fbs/internal/http"
	"github.com/i-got-this-faa/fbs/internal/metadata"
	"github.com/i-got-this-faa/fbs/internal/publicread"
)

func (h *Handlers) CreatePublicObjectURL(w http.ResponseWriter, r *http.Request) {
	bucketName := strings.TrimSpace(chi.URLParam(r, "bucket"))
	if !h.ensureBucket(w, r, bucketName) {
		return
	}

	key, ok := publicURLObjectKey(r)
	if !ok {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "object not found")
		return
	}

	if h.PublicReadSigner == nil {
		writeError(w, http.StatusServiceUnavailable, errorCodeInternal, "public read signing is not configured")
		return
	}

	request, ttl, ok := h.publicReadOptions(w, r)
	if !ok {
		return
	}

	if _, err := h.Objects.GetByKey(r.Context(), bucketName, key); errors.Is(err, metadata.ErrObjectNotFound) {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "object not found")
		return
	} else if err != nil {
		h.internalError(w, r, "failed to load object", err)
		return
	}

	expiresAt := time.Now().UTC().Add(ttl)
	path := publicread.ObjectPath(bucketName, key)
	expiresUnix := strconv.FormatInt(expiresAt.Unix(), 10)
	query := url.Values{}
	query.Set("expires", expiresUnix)
	if request.ResponseContentDisposition != "" {
		query.Set("response-content-disposition", request.ResponseContentDisposition)
	}
	query.Set("signature", h.PublicReadSigner.SignPath(path, expiresAt, request.ResponseContentDisposition))

	maxAge := int64(ttl / time.Second)
	writeJSON(w, http.StatusOK, publicObjectURLResponse{
		URL:          strings.TrimRight(h.publicBaseURL(r), "/") + path + "?" + query.Encode(),
		ExpiresAt:    formatTime(expiresAt),
		CacheControl: "public, max-age=" + strconv.FormatInt(maxAge, 10) + ", must-revalidate",
	})
}

func publicURLObjectKey(r *http.Request) (string, bool) {
	rawPath := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	key, ok := strings.CutSuffix(rawPath, "/public-url")
	if !ok || key == "" {
		return "", false
	}
	return key, true
}

func (h *Handlers) publicReadOptions(w http.ResponseWriter, r *http.Request) (publicObjectURLRequest, time.Duration, bool) {
	req := publicObjectURLRequest{}
	if err := httpapi.DecodeJSONBody(w, r, &req, httpapi.JSONBodyOptions{AllowEmpty: true}); err != nil {
		writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "invalid JSON body")
		return publicObjectURLRequest{}, 0, false
	}

	if req.ResponseContentDisposition != "" {
		if _, _, err := mime.ParseMediaType(req.ResponseContentDisposition); err != nil {
			writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "response_content_disposition is invalid")
			return publicObjectURLRequest{}, 0, false
		}
	}

	ttl := h.Config.PublicReadDefaultTTL
	if req.ExpiresInSeconds != nil {
		if *req.ExpiresInSeconds <= 0 {
			writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "expires_in_seconds must be positive")
			return publicObjectURLRequest{}, 0, false
		}
		if *req.ExpiresInSeconds > int64(h.Config.PublicReadMaxTTL/time.Second) {
			writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "expires_in_seconds exceeds maximum TTL")
			return publicObjectURLRequest{}, 0, false
		}
		ttl = time.Duration(*req.ExpiresInSeconds) * time.Second
	}
	if ttl <= 0 || ttl > h.Config.PublicReadMaxTTL {
		writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, "expires_in_seconds exceeds maximum TTL")
		return publicObjectURLRequest{}, 0, false
	}

	return req, ttl, true
}

func (h *Handlers) publicBaseURL(r *http.Request) string {
	if h.Config.PublicBaseURL != "" {
		return h.Config.PublicBaseURL
	}

	scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	return scheme + "://" + r.Host
}
