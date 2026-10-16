package metadata

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/i-got-this-faa/fbs/internal/iam"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestBucketDeleteWithObjectsCascadesDependents(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	bucket := insertTestBucket(t, db)
	buckets := NewBucketRepository(db)
	owner, err := buckets.GetByName(ctx, bucket)
	if err != nil {
		t.Fatal(err)
	}
	obj := newTestObject(bucket, "key")
	if err := NewObjectRepository(db).Create(ctx, obj); err != nil {
		t.Fatal(err)
	}
	grants := NewGrantRepository(db)
	grant := &Grant{ID: "grant", BucketName: bucket, GranteeUserID: owner.OwnerID, Action: iam.ActionGetObject, IsActive: true, CreatedBy: owner.OwnerID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := grants.Create(ctx, grant); err != nil {
		t.Fatal(err)
	}
	links := NewShareLinkRepository(db)
	if err := links.Create(ctx, &ShareLink{Code: "link", BucketName: bucket, ObjectKey: obj.Key, CreatedBy: owner.OwnerID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deleted, err := buckets.DeleteWithObjects(ctx, bucket)
	if err != nil || len(deleted) != 1 || deleted[0].StoragePath != obj.StoragePath {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
	if _, err := grants.GetByID(ctx, grant.ID); !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("grant lookup=%v, want not found", err)
	}
	if _, err := links.GetByCode(ctx, "link"); !errors.Is(err, ErrShareLinkNotFound) {
		t.Errorf("share link lookup=%v, want not found", err)
	}
	if _, err := buckets.DeleteWithObjects(ctx, bucket); !errors.Is(err, ErrBucketNotFound) {
		t.Errorf("repeat deletion=%v, want bucket not found", err)
	}
}

func TestBucketDeleteWithObjectsInvalidatesCacheOnSuccessAndFailure(t *testing.T) {
	for _, outcome := range []string{"success", "multipart conflict", "bucket delete failure"} {
		t.Run(outcome, func(t *testing.T) {
			db := openTestDB(t)
			ctx := context.Background()
			bucket := insertTestBucket(t, db)
			cache := NewMetadataCache(1 << 20)
			buckets := NewCachedBucketRepository(NewBucketRepository(db), cache)
			rawObjects := NewObjectRepository(db)
			objects := NewCachedObjectRepository(rawObjects, cache)
			for _, key := range []string{"one", "two"} {
				obj := newTestObject(bucket, key)
				if err := objects.Create(ctx, obj); err != nil {
					t.Fatal(err)
				}
				if _, err := objects.GetByKey(ctx, bucket, key); err != nil {
					t.Fatal(err)
				}
				obj.ETag = "updated outside cache"
				if err := rawObjects.Create(ctx, obj); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := buckets.GetByName(ctx, bucket); err != nil {
				t.Fatal(err)
			}
			if outcome == "multipart conflict" {
				if err := NewMultipartUploadRepository(db).Create(ctx, &MultipartUpload{ID: "pending", BucketName: bucket, Key: "pending"}); err != nil {
					t.Fatal(err)
				}
			}
			if outcome == "bucket delete failure" {
				if _, err := db.Exec(`CREATE TRIGGER fail_bucket_delete BEFORE DELETE ON buckets BEGIN SELECT RAISE(ABORT, 'forced delete failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			deleted, err := buckets.DeleteWithObjects(ctx, bucket)
			if outcome == "success" {
				if err != nil || len(deleted) != 2 {
					t.Fatalf("deleted=%v err=%v", deleted, err)
				}
				if _, err := buckets.GetByName(ctx, bucket); !errors.Is(err, ErrBucketNotFound) {
					t.Errorf("cached bucket survived deletion: %v", err)
				}
			} else {
				if err == nil || len(deleted) != 0 {
					t.Fatalf("failed deletion returned objects=%v err=%v", deleted, err)
				}
				if outcome == "multipart conflict" && !errors.Is(err, ErrBucketHasMultipartUploads) {
					t.Fatalf("conflict=%v", err)
				}
				if _, err := buckets.GetByName(ctx, bucket); err != nil {
					t.Errorf("failed deletion removed bucket: %v", err)
				}
			}
			for _, key := range []string{"one", "two"} {
				obj, err := objects.GetByKey(ctx, bucket, key)
				if outcome == "success" {
					if !errors.Is(err, ErrObjectNotFound) {
						t.Errorf("cached %s survived deletion: obj=%+v err=%v", key, obj, err)
					}
				} else if err != nil || obj.ETag != "updated outside cache" {
					t.Errorf("failed deletion must roll back objects and evict stale cache: obj=%+v err=%v", obj, err)
				}
			}
		})
	}
}

func TestBucketDeleteWithObjectsConcurrentUpload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	uploadDB, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = uploadDB.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner := insertTestUser(t, db)
	buckets := NewBucketRepository(db)
	objects := NewObjectRepository(db)
	uploads := NewMultipartUploadRepository(uploadDB)
	type deleteResult struct {
		objects []Object
		err     error
	}
	wins := map[string]int{}
	for i := range 64 {
		bucket := fmt.Sprintf("concurrent-%d", i)
		if err := buckets.Create(ctx, &Bucket{Name: bucket, OwnerID: owner, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		obj := newTestObject(bucket, "protected")
		if err := objects.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
		upload := &MultipartUpload{ID: bucket, BucketName: bucket, Key: "pending"}
		start := make(chan struct{})
		created := make(chan error, 1)
		deleted := make(chan deleteResult, 1)
		go func() {
			<-start
			created <- uploads.Create(ctx, upload)
		}()
		go func() {
			<-start
			removed, err := buckets.DeleteWithObjects(ctx, bucket)
			deleted <- deleteResult{removed, err}
		}()
		close(start)
		createErr, result := <-created, <-deleted
		switch {
		case createErr == nil:
			wins["upload"]++
			if !errors.Is(result.err, ErrBucketHasMultipartUploads) || len(result.objects) != 0 {
				t.Fatalf("upload committed, deletion must conflict without cleanup: result=%+v", result)
			}
			stored, err := objects.GetByKey(ctx, bucket, obj.Key)
			if err != nil || stored.ID != obj.ID {
				t.Fatalf("upload won but original object changed: object=%+v err=%v", stored, err)
			}
			if _, err := buckets.GetByName(ctx, bucket); err != nil {
				t.Fatalf("upload won but bucket changed: %v", err)
			}
			if _, err := uploads.GetByID(ctx, upload.ID); err != nil {
				t.Fatalf("committed upload disappeared: %v", err)
			}
		case result.err == nil:
			wins["delete"]++
			if len(result.objects) != 1 || result.objects[0].StoragePath != obj.StoragePath {
				t.Fatalf("cleanup must identify original object: %v", result.objects)
			}
			sqlErr, ok := errors.AsType[*sqlite.Error](createErr)
			if !ok || sqlErr.Code() != sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
				t.Fatalf("delete won, upload must fail its bucket foreign key: %v", createErr)
			}
			if _, err := buckets.GetByName(ctx, bucket); !errors.Is(err, ErrBucketNotFound) {
				t.Fatalf("deleted bucket survived: %v", err)
			}
			if _, err := uploads.GetByID(ctx, upload.ID); !errors.Is(err, ErrMultipartUploadNotFound) {
				t.Fatalf("upload references deleted bucket: %v", err)
			}
		default:
			t.Fatalf("unexpected concurrent outcome: create=%v delete=%v", createErr, result.err)
		}
	}
	t.Logf("independent-connection race outcomes: %v", wins)
}

func TestBucketDeleteWithObjectsWaitsForConcurrentUploadCommit(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bucket := insertTestBucket(t, db)
	obj := newTestObject(bucket, "protected")
	objects := NewObjectRepository(db)
	if err := objects.Create(ctx, obj); err != nil {
		t.Fatal(err)
	}
	uploadTx, err := beginImmediate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer uploadTx.close()
	if _, err := uploadTx.ExecContext(ctx, `INSERT INTO multipart_uploads(id, bucket_name, key, content_type, status) VALUES ('in-flight', ?, 'pending', 'text/plain', 'active')`, bucket); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	deleted := make(chan error, 1)
	go func() {
		close(started)
		_, err := NewBucketRepository(db).DeleteWithObjects(ctx, bucket)
		deleted <- err
	}()
	<-started
	select {
	case err := <-deleted:
		t.Fatalf("deletion must wait for concurrent writer, got %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := uploadTx.commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; !errors.Is(err, ErrBucketHasMultipartUploads) {
		t.Fatalf("committed upload must prevent deletion: %v", err)
	}
	stored, err := objects.GetByKey(ctx, bucket, obj.Key)
	if err != nil || stored.ID != obj.ID {
		t.Fatalf("concurrent upload must protect original object: object=%+v err=%v", stored, err)
	}
}
