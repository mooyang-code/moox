package sqlite

import (
	"context"
	"database/sql"
)

// immediateTx pins a connection and starts the write transaction explicitly.
// database/sql does not expose SQLite's BEGIN IMMEDIATE through TxOptions.
type immediateTx struct {
	conn   *sql.Conn
	ctx    context.Context
	closed bool
}

func beginImmediate(ctx context.Context, db *sql.DB) (*immediateTx, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	tx := &immediateTx{conn: conn, ctx: ctx}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tx, nil
}

func (tx *immediateTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.conn.ExecContext(ctx, query, args...)
}

func (tx *immediateTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.conn.QueryRowContext(ctx, query, args...)
}

func (tx *immediateTx) Commit() error {
	if tx == nil || tx.closed {
		return nil
	}
	_, err := tx.conn.ExecContext(tx.ctx, "COMMIT")
	closeErr := tx.close()
	if err != nil {
		return err
	}
	return closeErr
}

func (tx *immediateTx) Rollback() error {
	if tx == nil || tx.closed {
		return nil
	}
	_, err := tx.conn.ExecContext(context.Background(), "ROLLBACK")
	closeErr := tx.close()
	if err != nil {
		return err
	}
	return closeErr
}

func (tx *immediateTx) close() error {
	if tx.closed {
		return nil
	}
	tx.closed = true
	return tx.conn.Close()
}
