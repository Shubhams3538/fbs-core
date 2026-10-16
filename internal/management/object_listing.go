package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/i-got-this-faa/fbs/internal/metadata"
)

const defaultObjectListLimit = 100

const maxObjectListLimit = 1000

func (h *Handlers) ListObjects(w http.ResponseWriter, r *http.Request) {
	bucketName := strings.TrimSpace(chi.URLParam(r, "bucket"))
	if !h.ensureBucket(w, r, bucketName) {
		return
	}

	params, err := parseObjectListParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, err.Error())
		return
	}

	objects, commonPrefixes, isTruncated, nextCursor, err := h.listObjects(r, bucketName, params)
	if err != nil {
		h.internalError(w, r, "failed to list objects", err)
		return
	}

	writeJSON(w, http.StatusOK, listObjectsResponse{
		Bucket:         bucketName,
		Prefix:         params.prefix,
		Delimiter:      params.delimiter,
		Limit:          params.limit,
		IsTruncated:    isTruncated,
		NextCursor:     nextCursor,
		Objects:        objects,
		CommonPrefixes: commonPrefixes,
	})
}

func (h *Handlers) GetObject(w http.ResponseWriter, r *http.Request) {
	bucketName := strings.TrimSpace(chi.URLParam(r, "bucket"))
	if !h.ensureBucket(w, r, bucketName) {
		return
	}

	key := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	if key == "" {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "object not found")
		return
	}

	obj, err := h.Objects.GetByKey(r.Context(), bucketName, key)
	if errors.Is(err, metadata.ErrObjectNotFound) {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "object not found")
		return
	}
	if err != nil {
		h.internalError(w, r, "failed to load object", err)
		return
	}

	writeJSON(w, http.StatusOK, objectDetailResponse{Object: objectDetailDTO(*obj)})
}

type objectListParams struct {
	prefix    string
	delimiter string
	cursor    string
	limit     int
}

func parseObjectListParams(r *http.Request) (objectListParams, error) {
	query := r.URL.Query()
	limit := defaultObjectListLimit
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil || parsedLimit <= 0 {
			return objectListParams{}, errors.New("limit must be a positive integer")
		}
		limit = parsedLimit
	}
	if limit > maxObjectListLimit {
		limit = maxObjectListLimit
	}

	return objectListParams{
		prefix:    query.Get("prefix"),
		delimiter: query.Get("delimiter"),
		cursor:    query.Get("cursor"),
		limit:     limit,
	}, nil
}

func (h *Handlers) listObjects(r *http.Request, bucketName string, params objectListParams) ([]objectSummaryResponse, []string, bool, string, error) {
	if params.delimiter == "" {
		objects, isTruncated, err := h.Objects.List(r.Context(), bucketName, params.prefix, params.cursor, params.limit)
		if err != nil {
			return nil, nil, false, "", err
		}

		responseObjects := make([]objectSummaryResponse, 0, len(objects))
		for _, obj := range objects {
			responseObjects = append(responseObjects, objectSummaryDTO(obj))
		}

		nextCursor := ""
		if isTruncated && len(objects) > 0 {
			nextCursor = objects[len(objects)-1].Key
		}
		return responseObjects, []string{}, isTruncated, nextCursor, nil
	}

	entries, err := h.Objects.ListDelimited(r.Context(), bucketName, params.prefix, params.cursor, params.delimiter, params.limit)
	if err != nil {
		return nil, nil, false, "", err
	}

	isTruncated := len(entries) > params.limit
	if isTruncated {
		entries = entries[:params.limit]
	}

	objects := make([]objectSummaryResponse, 0, len(entries))
	commonPrefixes := make([]string, 0, len(entries))
	nextCursor := ""
	for _, entry := range entries {
		nextCursor = entry.CursorKey
		if entry.IsPrefix {
			commonPrefixes = append(commonPrefixes, entry.VirtualKey)
			continue
		}
		if entry.Object != nil {
			objects = append(objects, objectSummaryDTO(*entry.Object))
		}
	}
	if !isTruncated {
		nextCursor = ""
	}

	return objects, commonPrefixes, isTruncated, nextCursor, nil
}
