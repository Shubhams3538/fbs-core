package metadata

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/i-got-this-faa/fbs/internal/storage"
)

type pausedObjectLookup struct {
	ObjectRepository
	entered, release chan struct{}
	once             sync.Once
}

func (r *pausedObjectLookup) GetByKey(ctx context.Context, bucket, key string) (*Object, error) {
	obj, err := r.ObjectRepository.GetByKey(ctx, bucket, key)
	r.once.Do(func() { close(r.entered); <-r.release })
	return obj, err
}

func TestCacheMissCannotResurrectCommittedObjectVersion(t *testing.T) {
	for _, operation := range []string{"overwrite", "delete", "empty", "bucket delete", "external commit"} {
		t.Run(operation, func(t *testing.T) {
			db := openTestDB(t)
			bucket := insertTestBucket(t, db)
			raw := NewObjectRepository(db)
			ctx := context.Background()
			disk, err := storage.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			oldPath, _, err := disk.Write(ctx, bucket, "key", strings.NewReader("old"))
			if err != nil {
				t.Fatal(err)
			}
			old := testCacheObject(bucket, "key", "old")
			old.StoragePath = oldPath
			if err := raw.Create(ctx, old); err != nil {
				t.Fatal(err)
			}
			delayed := &pausedObjectLookup{ObjectRepository: raw, entered: make(chan struct{}), release: make(chan struct{})}
			cache := NewMetadataCache(1 << 20)
			repo := NewCachedObjectRepository(delayed, cache)
			var once sync.Once
			release := func() { once.Do(func() { close(delayed.release) }) }
			t.Cleanup(release)
			read := make(chan error, 1)
			go func() { _, err := repo.GetByKey(ctx, bucket, "key"); read <- err }()
			select {
			case <-delayed.entered:
			case <-time.After(time.Second):
				t.Fatal("lookup did not pause")
			}
			replacement := operation == "overwrite" || operation == "external commit"
			if replacement {
				path, _, err := disk.Write(ctx, bucket, "key", strings.NewReader("new"))
				if err != nil {
					t.Fatal(err)
				}
				obj := testCacheObject(bucket, "key", "new")
				obj.StoragePath = path
				if operation == "overwrite" {
					err = repo.Create(ctx, obj)
				} else {
					err = raw.Create(ctx, obj)
					InvalidateObjectCache(repo, bucket, "key")
				}
				if err != nil {
					t.Fatal(err)
				}
			} else {
				switch operation {
				case "delete":
					err = repo.Delete(ctx, bucket, "key")
				case "empty":
					err = repo.DeleteAllInBucket(ctx, bucket)
				case "bucket delete":
					if err := raw.DeleteAllInBucket(ctx, bucket); err != nil {
						t.Fatal(err)
					}
					err = NewCachedBucketRepository(NewBucketRepository(db), cache).Delete(ctx, bucket)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := disk.Delete(ctx, oldPath); err != nil {
				t.Fatal(err)
			}
			release()
			select {
			case <-read:
			case <-time.After(time.Second):
				t.Fatal("paused lookup did not return")
			}
			got, err := repo.GetByKey(ctx, bucket, "key")
			if !replacement {
				if !errors.Is(err, ErrObjectNotFound) {
					t.Fatalf("deleted object returned: %+v, %v", got, err)
				}
				return
			}
			if err != nil || got.ETag != "new" {
				t.Fatalf("stale cached object: %+v, %v", got, err)
			}
			file, err := disk.Open(ctx, got.StoragePath)
			if err != nil {
				t.Fatalf("committed object unreadable: %v", err)
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil || string(data) != "new" {
				t.Fatalf("bytes=%q error=%v", data, err)
			}
		})
	}
}

func TestCacheBudgetIncludesUserMetadataAndChecksums(t *testing.T) {
	cache := NewMetadataCache(1024)
	obj := testCacheObject("bucket", "metadata", "etag")
	obj.UserMetadata = map[string]string{"description": strings.Repeat("m", 128<<10)}
	cache.putObject(obj)
	if _, ok := cache.getObject(obj.BucketName, obj.Key); ok {
		t.Fatal("metadata larger than whole budget retained")
	}
	obj.UserMetadata = nil
	obj.ChecksumSHA256 = strings.Repeat("s", 1024)
	cache.putObject(obj)
	if _, ok := cache.getObject(obj.BucketName, obj.Key); ok {
		t.Fatal("checksum larger than whole budget retained")
	}
	if cache.used > cache.maxBytes {
		t.Fatalf("budget exceeded: %d > %d", cache.used, cache.maxBytes)
	}
}

func TestCachedObjectMetadataIsAnIndependentCopy(t *testing.T) {
	cache := NewMetadataCache(1 << 20)
	obj := testCacheObject("bucket", "key", "etag")
	obj.UserMetadata = map[string]string{"label": "original"}
	cache.putObject(obj)
	obj.UserMetadata["label"] = "mutated input"
	got, ok := cache.getObject("bucket", "key")
	if !ok || got.UserMetadata["label"] != "original" {
		t.Fatal("cache aliases caller metadata")
	}
	got.UserMetadata["label"] = "mutated result"
	got, ok = cache.getObject("bucket", "key")
	if !ok || got.UserMetadata["label"] != "original" {
		t.Fatal("cache aliases returned metadata")
	}
}

type pausedBucketLookup struct {
	BucketRepository
	entered, release chan struct{}
	once             sync.Once
}

func (r *pausedBucketLookup) GetByName(ctx context.Context, name string) (*Bucket, error) {
	bucket, err := r.BucketRepository.GetByName(ctx, name)
	r.once.Do(func() { close(r.entered); <-r.release })
	return bucket, err
}

func TestBucketCacheMissCannotRestoreOldOwner(t *testing.T) {
	db := openTestDB(t)
	bucket := insertTestBucket(t, db)
	raw := NewBucketRepository(db)
	ctx := context.Background()
	old, err := raw.GetByName(ctx, bucket)
	if err != nil {
		t.Fatal(err)
	}
	owner := newTestUser()
	owner.AccessKeyID += owner.ID
	owner.SigV4AccessKeyID += owner.ID
	if err := NewUserRepository(db).Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	delayed := &pausedBucketLookup{BucketRepository: raw, entered: make(chan struct{}), release: make(chan struct{})}
	repo := NewCachedBucketRepository(delayed, NewMetadataCache(1<<20))
	var once sync.Once
	release := func() { once.Do(func() { close(delayed.release) }) }
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() { _, err := repo.GetByName(ctx, bucket); done <- err }()
	select {
	case <-delayed.entered:
	case <-time.After(time.Second):
		t.Fatal("bucket lookup did not pause")
	}
	if err := repo.UpdateOwner(ctx, bucket, owner.ID, old.OwnerID); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bucket lookup did not resume")
	}
	got, err := repo.GetByName(ctx, bucket)
	if err != nil || got.OwnerID != owner.ID {
		t.Fatalf("old bucket owner restored: %+v, %v", got, err)
	}
}
