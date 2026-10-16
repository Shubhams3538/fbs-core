package management_test

import (
	"context"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/i-got-this-faa/fbs/internal/metadata"
)

func TestManagementDeleteBucketRejectsMultipartWithoutDeletingData(t *testing.T) {
	for _, status := range []metadata.MultipartUploadStatus{
		metadata.MultipartUploadStatusActive,
		metadata.MultipartUploadStatusCompleting,
		metadata.MultipartUploadStatusAborted,
	} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			env := newManagementTestEnv(t)
			paths := seedBucketCleanupFiles(t, env)
			ctx := context.Background()
			objects := metadata.NewObjectRepository(env.db)
			before, _, err := objects.List(ctx, "photos", "", "", 100)
			if err != nil {
				t.Fatal(err)
			}
			uploads := metadata.NewMultipartUploadRepository(env.db)
			upload := &metadata.MultipartUpload{ID: "pending-upload", BucketName: "photos", Key: "pending.bin"}
			if err := uploads.Create(ctx, upload); err != nil {
				t.Fatal(err)
			}
			partPath, size, err := env.storage.WritePart(ctx, upload.ID, 1, strings.NewReader("part content"))
			if err != nil {
				t.Fatal(err)
			}
			part := &metadata.MultipartPart{UploadID: upload.ID, PartNumber: 1, Size: size, ETag: "etag", StoragePath: partPath}
			if _, err := uploads.AddPart(ctx, part); err != nil {
				t.Fatal(err)
			}
			if err := uploads.SetUploadStatus(ctx, upload.ID, status); err != nil {
				t.Fatal(err)
			}
			resp := env.do(t, http.MethodDelete, "/api/management/buckets/photos", env.adminToken, nil)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusConflict {
				t.Errorf("delete status=%d, want 409", resp.StatusCode)
			}
			if _, err := metadata.NewBucketRepository(env.db).GetByName(ctx, "photos"); err != nil {
				t.Errorf("bucket changed: %v", err)
			}
			after, _, err := objects.List(ctx, "photos", "", "", 100)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Errorf("objects changed on conflict: before=%v after=%v err=%v", before, after, err)
			}
			for _, path := range paths {
				body, err := os.ReadFile(path)
				if err != nil || string(body) != "stored object body" {
					t.Errorf("object file changed: body=%q err=%v", body, err)
				}
			}
			stored, err := uploads.GetByID(ctx, upload.ID)
			if err != nil || stored.Status != status {
				t.Fatalf("upload changed: upload=%+v err=%v", stored, err)
			}
			parts, err := uploads.ListParts(ctx, upload.ID)
			if err != nil || len(parts) != 1 || parts[0].StoragePath != partPath {
				t.Errorf("parts changed: parts=%v err=%v", parts, err)
			}
			file, err := env.storage.Open(ctx, partPath)
			if err != nil {
				t.Fatalf("part file removed: %v", err)
			}
			_ = file.Close()
		})
	}
}
