package management_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/i-got-this-faa/fbs/internal/auth"
	"github.com/i-got-this-faa/fbs/internal/iam"
	"github.com/i-got-this-faa/fbs/internal/management"
	"github.com/i-got-this-faa/fbs/internal/metadata"
	"github.com/i-got-this-faa/fbs/internal/storage"
)

type cancelAfterBucketDelete struct {
	metadata.BucketRepository
	cancel context.CancelFunc
}

func (r cancelAfterBucketDelete) DeleteWithObjects(ctx context.Context, name string) ([]metadata.Object, error) {
	objects, err := r.BucketRepository.DeleteWithObjects(ctx, name)
	if err == nil {
		r.cancel()
	}
	return objects, err
}

type bucketCleanupStorage struct {
	storage.DiskEngine
	t        *testing.T
	failPath string
	attempts map[string]int
}

func (s *bucketCleanupStorage) Delete(ctx context.Context, path string) error {
	s.attempts[path]++
	if err := ctx.Err(); err != nil {
		s.t.Errorf("postcommit cleanup inherited request cancellation: %v", err)
	}
	if _, ok := ctx.Deadline(); !ok {
		s.t.Error("postcommit cleanup must have a deadline")
	}
	if path == s.failPath {
		return errors.New("forced file deletion failure")
	}
	return s.DiskEngine.Delete(ctx, path)
}

func TestManagementDeleteBucketCleansFilesAfterCommit(t *testing.T) {
	for _, failFile := range []bool{false, true} {
		name := "canceled request"
		if failFile {
			name = "canceled request with file failure"
		}
		t.Run(name, func(t *testing.T) {
			env := newManagementTestEnv(t)
			paths := seedBucketCleanupFiles(t, env)
			objects := metadata.NewObjectRepository(env.db)
			before, _, err := objects.List(context.Background(), "photos", "", "", 100)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			disk := &bucketCleanupStorage{DiskEngine: env.storage, t: t, attempts: make(map[string]int)}
			if failFile {
				obj, err := objects.GetByKey(ctx, "photos", "stored.jpg")
				if err != nil {
					t.Fatal(err)
				}
				disk.failPath = obj.StoragePath
			}
			h := &management.Handlers{
				Buckets:  cancelAfterBucketDelete{metadata.NewBucketRepository(env.db), cancel},
				Objects:  objects,
				Storage:  disk,
				Activity: metadata.NewActivityRepository(env.db),
				Logger:   slog.New(slog.NewTextHandler(env.logs, nil)),
			}
			route := chi.NewRouteContext()
			route.URLParams.Add("bucket", "photos")
			req := httptest.NewRequest(http.MethodDelete, "/api/management/buckets/photos", nil)
			ctx = auth.WithPrincipal(ctx, auth.Principal{UserID: "owner", Role: iam.RoleAdmin})
			req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
			resp := httptest.NewRecorder()
			h.DeleteBucket(resp, req)
			wantStatus := http.StatusNoContent
			if failFile {
				wantStatus = http.StatusInternalServerError
			}
			if resp.Code != wantStatus {
				t.Errorf("status=%d, want %d: %s", resp.Code, wantStatus, resp.Body.String())
			}
			if ctx.Err() == nil {
				t.Fatal("request was not canceled after commit")
			}
			activities, err := h.Activity.List(context.Background(), metadata.ActivityListFilter{BucketName: "photos", Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			deletedKeys := make(map[string]bool)
			forced := 0
			for _, activity := range activities {
				if activity.ActorUserID != "owner" {
					t.Errorf("postcommit activity lost actor: %+v", activity)
				}
				switch activity.Action {
				case metadata.ActivityDeleteObject:
					deletedKeys[activity.ObjectKey] = true
				case metadata.ActivityForceDeleteBucket:
					forced++
					if activity.Size != int64(len(before)) {
						t.Errorf("force-delete activity count=%d, want %d", activity.Size, len(before))
					}
				}
			}
			if forced != 1 || len(deletedKeys) != len(before) {
				t.Errorf("postcommit activity missing after cancellation: force=%d deleted=%v", forced, deletedKeys)
			}
			for _, obj := range before {
				if !deletedKeys[obj.Key] {
					t.Errorf("missing committed deletion activity for %q", obj.Key)
				}
			}
			for _, obj := range before {
				if disk.attempts[obj.StoragePath] != 1 {
					t.Errorf("cleanup attempts for %q=%d, want 1", obj.StoragePath, disk.attempts[obj.StoragePath])
				}
			}
			if _, err := metadata.NewBucketRepository(env.db).GetByName(context.Background(), "photos"); !errors.Is(err, metadata.ErrBucketNotFound) {
				t.Errorf("bucket metadata not committed: %v", err)
			}
			for i, path := range paths {
				_, err := os.Stat(path)
				if failFile && i == 0 {
					if err != nil {
						t.Errorf("injected failure did not leave file: %v", err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("cleanup skipped file %q: %v", path, err)
				}
			}
			if failFile {
				if env.logs.Len() == 0 {
					t.Error("file cleanup failure was not logged")
				}
				if err := env.storage.Reconcile(context.Background(), func(bucket string) ([]string, error) {
					remaining, _, err := objects.List(context.Background(), bucket, "", "", 100)
					var known []string
					for _, obj := range remaining {
						known = append(known, obj.StoragePath)
					}
					return known, err
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(paths[0]); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("reconciliation did not remove orphan: %v", err)
				}
			}
		})
	}
}
