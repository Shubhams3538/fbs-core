package metadata

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

func TestImmediateTransactionRollbackSurvivesCancellation(t *testing.T) {
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	bucket := insertTestBucket(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := beginImmediate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	obj := testCacheObject(bucket, "uncommitted", "etag")
	if err := upsertObject(ctx, tx, obj); err != nil {
		tx.close()
		t.Fatal(err)
	}
	cancel()
	tx.close()
	repo := NewObjectRepository(db)
	if _, err := repo.GetByKey(context.Background(), bucket, obj.Key); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("canceled transaction committed: %v", err)
	}
	ack := testCacheObject(bucket, "acknowledged", "etag")
	if err := repo.Create(context.Background(), ack); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ROLLBACK"); err == nil {
		t.Fatal("transaction leaked into pooled connection")
	}
	if _, err := repo.GetByKey(context.Background(), bucket, ack.Key); err != nil {
		t.Fatalf("acknowledged write lost: %v", err)
	}
}

func TestFailedTransactionCleanupDiscardsConnection(t *testing.T) {
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	tx, err := beginImmediate(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a driver-side loss of transaction state before deferred cleanup.
	if _, err := tx.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	tx.close()
	if got := db.Stats().Idle; got != 0 {
		t.Fatalf("failed-cleanup connection returned to pool: idle=%d", got)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("replacement connection: %v", err)
	}
}

// Deliver ordinary context cancellation deterministically at database/sql's
// context checks, covering cancellation on both sides of BEGIN and COMMIT.
// No production hooks or scheduler-dependent microsecond sleeps are required.
type cancelAtBoundary struct {
	context.Context
	cancel    context.CancelFunc
	remaining atomic.Int32
}

func (c *cancelAtBoundary) Done() <-chan struct{} {
	if c.remaining.Add(-1) == 0 {
		c.cancel()
	}
	return c.Context.Done()
}

func TestManualTransactionsNeverPoisonNextWrite(t *testing.T) {
	for _, operation := range []string{"bootstrap", "claim", "part", "complete"} {
		t.Run(operation, func(t *testing.T) {
			for boundary := int32(1); boundary <= 40; boundary++ {
				t.Run(fmt.Sprint(boundary), func(t *testing.T) {
					db := openTestDB(t)
					db.SetMaxOpenConns(1)
					bucket := insertTestBucket(t, db)
					repo := NewMultipartUploadRepository(db)
					upload := newTestMultipartUpload(bucket)
					if err := repo.Create(context.Background(), upload); err != nil {
						t.Fatal(err)
					}
					if operation == "complete" {
						if err := repo.ClaimUpload(context.Background(), upload.ID, MultipartUploadStatusCompleting); err != nil {
							t.Fatal(err)
						}
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					probe := &cancelAtBoundary{Context: ctx, cancel: cancel}
					probe.remaining.Store(boundary)
					switch operation {
					case "bootstrap":
						_ = NewBootstrapRepository(db).CreateFirstUser(probe, newTestUser())
					case "claim":
						_ = repo.ClaimUpload(probe, upload.ID, MultipartUploadStatusAborted)
					case "part":
						_, _ = repo.AddPart(probe, newTestMultipartPart(upload.ID, 1))
					case "complete":
						_, _ = repo.CompleteUpload(probe, testCacheObject(bucket, "complete", "etag"), upload.ID, "", "")
					}
					cancel()
					objects := NewObjectRepository(db)
					ack := testCacheObject(bucket, "acknowledged", "etag")
					if err := objects.Create(context.Background(), ack); err != nil {
						t.Fatalf("next write: %v", err)
					}
					// A fresh transaction must succeed and must not roll back the acknowledged row.
					tx, err := beginImmediate(context.Background(), db)
					if err != nil {
						t.Fatalf("next transaction: %v", err)
					}
					tx.close()
					if _, err := objects.GetByKey(context.Background(), bucket, ack.Key); err != nil {
						t.Fatalf("acknowledged write lost: %v", err)
					}
				})
			}
		})
	}
}
