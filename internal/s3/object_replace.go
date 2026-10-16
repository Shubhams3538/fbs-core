package s3

import (
	"errors"
	"net/http"

	"github.com/i-got-this-faa/fbs/internal/metadata"
)

func (h *ObjectHandlers) writeObjectReplaceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, metadata.ErrPreconditionFailed):
		WriteS3Error(w, r, http.StatusPreconditionFailed, codePreconditionFailed, messagePreconditionFailed)
	case errors.Is(err, metadata.ErrObjectNotFound):
		WriteS3Error(w, r, http.StatusNotFound, codeNoSuchKey, messageNoSuchKey)
	default:
		WriteS3Error(w, r, http.StatusInternalServerError, codeInternalError, messageInternalError)
	}
}
