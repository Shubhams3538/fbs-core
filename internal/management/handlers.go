package management

import (
	"log/slog"
	"net/http"

	"github.com/i-got-this-faa/fbs/internal/config"
	"github.com/i-got-this-faa/fbs/internal/metadata"
	"github.com/i-got-this-faa/fbs/internal/publicread"
	"github.com/i-got-this-faa/fbs/internal/s3compat"
	"github.com/i-got-this-faa/fbs/internal/storage"
)

type Handlers struct {
	Management       metadata.ManagementRepository
	Buckets          metadata.BucketRepository
	Objects          metadata.ObjectRepository
	Activity         metadata.ActivityRepository
	Users            metadata.UserRepository
	Grants           metadata.GrantRepository
	Storage          storage.DiskEngine
	Config           config.Config
	PublicReadSigner *publicread.Signer
	ShareLinks       metadata.ShareLinkRepository
	Logger           *slog.Logger
}

func (h *Handlers) Metrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := h.Management.Metrics(r.Context())
	if err != nil {
		h.internalError(w, r, "failed to load metrics", err)
		return
	}

	writeJSON(w, http.StatusOK, metricsResponse{
		BucketCount:      metrics.BucketCount,
		ObjectCount:      metrics.ObjectCount,
		TotalObjectBytes: metrics.TotalObjectBytes,
		UserCount:        metrics.UserCount,
		ActiveUserCount:  metrics.ActiveUserCount,
	})
}

func (h *Handlers) ConfigInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, configResponse{
		Region:        s3compat.Region,
		DevMode:       h.Config.DevMode,
		PublicBaseURL: h.Config.PublicBaseURL,
		Limits: configLimitsResponse{
			S3MaxKeys:                 1000,
			S3DeleteObjects:           1000,
			ManagementObjectListLimit: maxObjectListLimit,
			ManagementActivityLimit:   maxActivityLimit,
		},
	})
}
