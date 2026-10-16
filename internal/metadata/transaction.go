package metadata

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"
)

const transactionCleanupTimeout = 5 * time.Second

// immediateTransaction owns its connection until commit or independent rollback.
// database/sql cannot manage a transaction started with a manual BEGIN for us.
type immediateTransaction struct {
	*sql.Conn
	committed bool
}

func beginImmediate(ctx context.Context, db *sql.DB) (*immediateTransaction, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		// Cancellation can leave the result of BEGIN uncertain. Never reuse it.
		discardConnection(conn)
		_ = conn.Close()
		return nil, err
	}
	return &immediateTransaction{Conn: conn}, nil
}

func (tx *immediateTransaction) commit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Once COMMIT starts, cancellation must not turn a persisted write into an
	// error that makes callers delete its backing file. A context timeout here
	// has the same ambiguity; SQLite's busy_timeout bounds lock waits instead.
	if _, err := tx.ExecContext(context.WithoutCancel(ctx), "COMMIT"); err != nil {
		return err
	}
	tx.committed = true
	return nil
}

func (tx *immediateTransaction) close() {
	if !tx.committed {
		ctx, cancel := context.WithTimeout(context.Background(), transactionCleanupTimeout)
		defer cancel()
		if _, err := tx.ExecContext(ctx, "ROLLBACK"); err != nil {
			discardConnection(tx.Conn)
		}
	}
	_ = tx.Conn.Close()
}

func discardConnection(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}
