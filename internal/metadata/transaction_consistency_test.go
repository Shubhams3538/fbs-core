package metadata

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"
)

func TestImmediateTransactionCanceledBeforeCommitRollsBack(t *testing.T) {
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	bucket := insertTestBucket(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := beginImmediate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.close()
	obj := testCacheObject(bucket, "canceled-before-commit", "etag")
	if err := upsertObject(ctx, tx, obj); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := tx.commit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("commit error = %v, want context.Canceled", err)
	}
	if tx.committed {
		t.Fatal("canceled transaction marked committed")
	}
	tx.close()
	objects := NewObjectRepository(db)
	if _, err := objects.GetByKey(context.Background(), bucket, obj.Key); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("canceled write persisted: %v", err)
	}
	if err := objects.Create(context.Background(), testCacheObject(bucket, "next-write", "etag")); err != nil {
		t.Fatalf("next write after rollback: %v", err)
	}
}

// Wrap the real SQLite driver to deliver cancellation at both ends of COMMIT.
// The post-COMMIT check models modernc's cancellation override after sqlite3_step.
type commitBoundaryConnector struct {
	driver driver.Driver
	dsn    string
	before func()
	after  func()
}

func (c commitBoundaryConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &commitBoundaryConn{Conn: conn, before: c.before, after: c.after}, nil
}

func (c commitBoundaryConnector) Driver() driver.Driver { return c.driver }

type commitBoundaryConn struct {
	driver.Conn
	before func()
	after  func()
}

func (c *commitBoundaryConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query == "COMMIT" && c.before != nil {
		c.before()
	}
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if query == "COMMIT" && err == nil {
		if c.after != nil {
			c.after()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return result, err
}

func TestImmediateTransactionCancellationAtCommitKeepsResultConsistent(t *testing.T) {
	for _, phase := range []string{"before-sqlite-step", "after-sqlite-commit"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "commit.db")
			initial, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			drv := initial.Driver()
			if err := initial.Close(); err != nil {
				t.Fatal(err)
			}
			dsn, err := sqliteDSN(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			connector := commitBoundaryConnector{driver: drv, dsn: dsn}
			called := false
			cancelAtCommit := func() {
				called = true
				cancel()
			}
			if phase == "before-sqlite-step" {
				connector.before = cancelAtCommit
			} else {
				connector.after = cancelAtCommit
			}
			db := sql.OpenDB(connector)
			t.Cleanup(func() { _ = db.Close() })
			db.SetMaxOpenConns(1)
			bucket := insertTestBucket(t, db)
			tx, err := beginImmediate(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.close()
			obj := testCacheObject(bucket, "committed", "etag")
			if err := upsertObject(ctx, tx, obj); err != nil {
				t.Fatal(err)
			}
			if err := tx.commit(ctx); err != nil {
				t.Fatalf("commit misreported cancellation: %v", err)
			}
			if !called || !errors.Is(ctx.Err(), context.Canceled) || !tx.committed {
				t.Fatalf("commit boundary not exercised: called=%v, canceled=%v, committed=%v", called, ctx.Err(), tx.committed)
			}
			tx.close()
			if db.Stats().InUse != 0 || db.Stats().Idle != 1 {
				t.Fatalf("committed connection not released: %+v", db.Stats())
			}
			objects := NewObjectRepository(db)
			if _, err := objects.GetByKey(context.Background(), bucket, obj.Key); err != nil {
				t.Fatalf("successful commit did not persist: %v", err)
			}
			next, err := beginImmediate(context.Background(), db)
			if err != nil {
				t.Fatalf("next transaction: %v", err)
			}
			next.close()
			if _, err := objects.GetByKey(context.Background(), bucket, obj.Key); err != nil {
				t.Fatalf("next rollback lost committed row: %v", err)
			}
		})
	}
}

func TestImmediateTransactionCommitFailureRollsBack(t *testing.T) {
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE commit_parent (id INTEGER PRIMARY KEY);
		CREATE TABLE commit_child (parent_id INTEGER REFERENCES commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	tx, err := beginImmediate(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.close()
	if _, err := tx.ExecContext(context.Background(), "INSERT INTO commit_child VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if err := tx.commit(context.Background()); err == nil {
		t.Fatal("commit unexpectedly accepted deferred foreign-key violation")
	}
	if tx.committed {
		t.Fatal("failed transaction marked committed")
	}
	tx.close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM commit_child").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed commit retained %d rows", count)
	}
	next, err := beginImmediate(context.Background(), db)
	if err != nil {
		t.Fatalf("next transaction: %v", err)
	}
	next.close()
}
