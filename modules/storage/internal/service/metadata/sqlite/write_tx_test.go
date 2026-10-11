package sqlite

import (
	"context"
	"testing"
)

func TestImmediateTxRollbackAfterCancellationReleasesConnection(t *testing.T) {
	base := context.Background()
	store := openTestStore(t, base)
	if _, err := store.db.ExecContext(base, `CREATE TABLE t_tx_probe (c_id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for name, finish := range map[string]func(tx *immediateTx) error{
		"rollback": func(tx *immediateTx) error { return tx.Rollback() },
		"commit":   func(tx *immediateTx) error { return tx.Commit() },
	} {
		ctx, cancel := context.WithCancel(base)
		tx, err := beginImmediate(ctx, store.db)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO t_tx_probe (c_id) VALUES (?)`, len(name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cancel() // 请求在两条 SQL 之间取消
		_ = finish(tx)

		// 单连接池：遗留事务会让下一次 BEGIN IMMEDIATE 失败
		next, err := beginImmediate(base, store.db)
		if err != nil {
			t.Fatalf("%s: 取消后无法重新开启事务: %v", name, err)
		}
		if err := next.Rollback(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var count int
		if err := store.db.QueryRowContext(base, `SELECT COUNT(*) FROM t_tx_probe`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s: 取消后的写入不应提交: count=%d err=%v", name, count, err)
		}
	}
}
