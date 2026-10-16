package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/i-got-this-faa/fbs/internal/metadata"
	"github.com/i-got-this-faa/fbs/internal/objectops"
)

const bucketFileCleanupTimeout = 5 * time.Second

func (h *Handlers) ListBuckets(w http.ResponseWriter, r *http.Request) {
	summaries, err := h.Management.ListBucketSummaries(r.Context())
	if err != nil {
		h.internalError(w, r, "failed to list buckets", err)
		return
	}

	buckets := make([]bucketSummaryResponse, 0, len(summaries))
	for _, summary := range summaries {
		buckets = append(buckets, bucketSummaryDTO(summary))
	}

	writeJSON(w, http.StatusOK, bucketsResponse{Buckets: buckets})
}

func (h *Handlers) GetBucket(w http.ResponseWriter, r *http.Request) {
	bucketName := strings.TrimSpace(chi.URLParam(r, "bucket"))
	if bucketName == "" {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "bucket not found")
		return
	}

	summary, err := h.Management.GetBucketSummary(r.Context(), bucketName)
	if errors.Is(err, metadata.ErrBucketNotFound) {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "bucket not found")
		return
	}
	if err != nil {
		h.internalError(w, r, "failed to load bucket", err)
		return
	}

	writeJSON(w, http.StatusOK, bucketResponse{Bucket: bucketSummaryDTO(summary)})
}

func (h *Handlers) DeleteBucket(w http.ResponseWriter, r *http.Request) {
	bucketName := strings.TrimSpace(chi.URLParam(r, "bucket"))
	if !h.ensureBucket(w, r, bucketName) {
		return
	}

	objects, err := h.Buckets.DeleteWithObjects(r.Context(), bucketName)
	if errors.Is(err, metadata.ErrBucketHasMultipartUploads) {
		writeError(w, http.StatusConflict, "bucket_has_multipart_uploads", "bucket has multipart uploads; complete or abort them before deleting the bucket")
		return
	}
	if errors.Is(err, metadata.ErrBucketNotFound) {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "bucket not found")
		return
	}
	if err != nil {
		h.internalError(w, r, "failed to delete bucket", err)
		return
	}
	cleanupErr := h.deleteBucketFiles(r.Context(), objects)
	for _, obj := range objects {
		h.recordActivity(r, metadata.ActivityDeleteObject, bucketName, obj.Key, obj.Size, obj.ETag)
	}
	h.recordActivity(r, metadata.ActivityForceDeleteBucket, bucketName, "", int64(len(objects)), "")
	if cleanupErr != nil {
		h.internalError(w, r, "failed to delete bucket object files", cleanupErr)
		return
	}

	setNoStoreHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

// Metadata has committed, so client cancellation must not skip file cleanup.
func (h *Handlers) deleteBucketFiles(ctx context.Context, objects []metadata.Object) error {
	if h.Storage == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bucketFileCleanupTimeout)
	defer cancel()
	var cleanupErr error
	for _, obj := range objects {
		if err := h.Storage.Delete(ctx, obj.StoragePath); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete %s/%s: %w", obj.BucketName, obj.Key, err))
		}
	}
	return cleanupErr
}

func (h *Handlers) EmptyBucket(w http.ResponseWriter, r *http.Request) {
	bucketName := strings.TrimSpace(chi.URLParam(r, "bucket"))
	if !h.ensureBucket(w, r, bucketName) {
		return
	}

	objects, err := objectops.EmptyBucket(r.Context(), h.Objects, h.Storage, bucketName)
	if err != nil {
		h.internalError(w, r, "failed to empty bucket", err)
		return
	}

	for _, obj := range objects {
		h.recordActivity(r, metadata.ActivityDeleteObject, bucketName, obj.Key, obj.Size, obj.ETag)
	}
	h.recordActivity(r, metadata.ActivityEmptyBucket, bucketName, "", int64(len(objects)), "")

	setNoStoreHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) ensureBucket(w http.ResponseWriter, r *http.Request, bucketName string) bool {
	if bucketName == "" {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "bucket not found")
		return false
	}

	_, err := h.Buckets.GetByName(r.Context(), bucketName)
	if errors.Is(err, metadata.ErrBucketNotFound) {
		writeError(w, http.StatusNotFound, errorCodeNotFound, "bucket not found")
		return false
	}
	if err != nil {
		h.internalError(w, r, "failed to load bucket", err)
		return false
	}

	return true
}
