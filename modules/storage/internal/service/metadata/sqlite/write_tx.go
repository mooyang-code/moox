package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"
)

// txCleanupTimeout 是取消后回滚事务的独立期限：请求 context 已取消时仍必须真正执行 ROLLBACK。
const txCleanupTimeout = 5 * time.Second

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

func (tx *immediateTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.conn.QueryContext(ctx, query, args...)
}

func (tx *immediateTx) Commit() error {
	if tx == nil || tx.closed {
		return nil
	}
	if _, err := tx.conn.ExecContext(tx.ctx, "COMMIT"); err != nil {
		// 提交失败（含请求取消）：事务可能仍处于打开状态，必须回滚后才能归还连接
		_ = tx.rollbackDetached()
		return err
	}
	return tx.close()
}

func (tx *immediateTx) Rollback() error {
	if tx == nil || tx.closed {
		return nil
	}
	return tx.rollbackDetached()
}

// rollbackDetached 用不受请求取消影响的有界 context 执行 ROLLBACK；
// 回滚失败时底层连接状态未知，必须废弃而不是归还连接池。
func (tx *immediateTx) rollbackDetached() error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(tx.ctx), txCleanupTimeout)
	defer cancel()
	_, err := tx.conn.ExecContext(cleanupCtx, "ROLLBACK")
	if err != nil {
		_ = tx.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
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
