package management

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/i-got-this-faa/fbs/internal/metadata"
	"github.com/i-got-this-faa/fbs/internal/storage"
)

type failedBucketCleanup struct {
	storage.DiskEngine
	t        *testing.T
	attempts int
	deadline time.Time
}

func (disk *failedBucketCleanup) Delete(ctx context.Context, _ string) error {
	disk.attempts++
	deadline, ok := ctx.Deadline()
	if !ok {
		disk.t.Error("file cleanup has no deadline")
	}
	if disk.deadline.IsZero() {
		disk.deadline = deadline
	} else if !deadline.Equal(disk.deadline) {
		disk.t.Error("cleanup extended its total budget for another file")
	}
	return context.DeadlineExceeded
}

func TestBucketFileCleanupBoundsFailureDetails(t *testing.T) {
	const count = 1000
	objects := make([]metadata.Object, count)
	for index := range objects {
		objects[index] = metadata.Object{BucketName: "bucket", Key: fmt.Sprintf("key-%d", index), StoragePath: fmt.Sprintf("path-%d", index)}
	}
	disk := &failedBucketCleanup{t: t}
	handlers := &Handlers{Storage: disk}
	err := handlers.deleteBucketFiles(context.Background(), objects)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup error=%v, want DeadlineExceeded", err)
	}
	if disk.attempts != count {
		t.Fatalf("cleanup attempts=%d, want %d", disk.attempts, count)
	}
	if !strings.Contains(err.Error(), "1000 of 1000") || !strings.Contains(err.Error(), "bucket/key-0") {
		t.Errorf("cleanup error lacks count or first failure: %v", err)
	}
	if len(err.Error()) > 1024 {
		t.Errorf("cleanup error grows with failed files: %d bytes", len(err.Error()))
	}
}
