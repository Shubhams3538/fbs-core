package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/i-got-this-faa/fbs/internal/metadata"
	"github.com/i-got-this-faa/fbs/internal/objectops"
)

const defaultActivityLimit = 100

const maxActivityLimit = 500

func (h *Handlers) ListActivity(w http.ResponseWriter, r *http.Request) {
	if h.Activity == nil {
		writeJSON(w, http.StatusOK, activityResponse{Activity: []activityItemResponse{}})
		return
	}

	params, err := parseActivityParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, errorCodeInvalidRequest, err.Error())
		return
	}

	activities, err := h.Activity.List(r.Context(), metadata.ActivityListFilter{
		BucketName: params.bucket,
		Action:     params.action,
		Limit:      params.limit,
	})
	if err != nil {
		h.internalError(w, r, "failed to list activity", err)
		return
	}

	responseItems := make([]activityItemResponse, 0, len(activities))
	for _, activity := range activities {
		responseItems = append(responseItems, activityDTO(activity))
	}

	writeJSON(w, http.StatusOK, activityResponse{Activity: responseItems})
}

type activityParams struct {
	limit  int
	bucket string
	action metadata.ActivityAction
}

func parseActivityParams(r *http.Request) (activityParams, error) {
	query := r.URL.Query()
	limit := defaultActivityLimit
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil || parsedLimit <= 0 {
			return activityParams{}, errors.New("limit must be a positive integer")
		}
		limit = parsedLimit
	}
	if limit > maxActivityLimit {
		limit = maxActivityLimit
	}

	return activityParams{
		limit:  limit,
		bucket: strings.TrimSpace(query.Get("bucket")),
		action: metadata.ActivityAction(strings.TrimSpace(query.Get("action"))),
	}, nil
}

// recordActivity audits a completed operation. Activity is not part of the
// operation, so a storage failure is logged and the request still succeeds.
func (h *Handlers) recordActivity(r *http.Request, action metadata.ActivityAction, bucketName, key string, size int64, etag string) {
	err := objectops.RecordActivity(r.Context(), h.Activity, metadata.ObjectActivity{
		ID:         uuid.New().String(),
		Action:     action,
		BucketName: bucketName,
		ObjectKey:  key,
		Size:       size,
		ETag:       etag,
		CreatedAt:  time.Now().UTC(),
	})
	if err != nil {
		h.logger().Error("record object activity", "error", err, "action", action, "bucket", bucketName, "key", key)
	}
}
