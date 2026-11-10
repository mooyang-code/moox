package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 第二轮审查批 7 删除了 t_replay_orders 表和 t_paper_balance_projections.c_applied_fill_count 列。
// 启动时的表结构校验要求库与当前 schema 完全一致，所以生产库必须先手工执行下面的 SQL 再启动新二进制：
// 旧库里还留着这张表或这一列时，新二进制拒绝启动（fail fast），不会带着错误的结构运行。
// SQLite 的 DROP COLUMN 不能删被表级 CHECK 引用的列，只能重建表。
// 生产库手工执行完成后，这个测试连同 legacy 常量一起删除。
const (
	legacyProjectionsDDL = `CREATE TABLE t_paper_balance_projections_legacy (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_applied_fill_count INTEGER NOT NULL DEFAULT 0,
    c_initialized_at INTEGER NOT NULL,
    PRIMARY KEY (c_space_id, c_trading_account_id),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_paper_account_configs (c_space_id, c_trading_account_id),
    CHECK (c_applied_fill_count >= 0),
    CHECK (c_initialized_at > 0)
)`
	legacyReplayOrdersDDL = `CREATE TABLE IF NOT EXISTS t_replay_orders (
    c_replay_id TEXT NOT NULL,
    c_order_id TEXT NOT NULL,
    c_period INTEGER NOT NULL,
    c_instrument_id TEXT NOT NULL,
    c_side TEXT NOT NULL,
    c_requested_quantity TEXT NOT NULL,
    c_filled_quantity TEXT NOT NULL,
    c_price TEXT NOT NULL,
    c_fee TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (c_replay_id, c_order_id),
    FOREIGN KEY (c_replay_id)
        REFERENCES t_replay_sessions (c_replay_id)
        ON DELETE CASCADE,
    CHECK (c_side IN ('BUY', 'SELL')),
    CHECK (c_status IN ('FILLED', 'PARTIAL', 'REJECTED'))
);
CREATE INDEX IF NOT EXISTS idx_replay_orders_period
ON t_replay_orders (c_replay_id, c_period)`

	// 生产库手工 SQL（先停服、备份）：重建投影表去掉旧列，再删回放订单表。
	manualDropLegacySQL = `PRAGMA foreign_keys = OFF;
BEGIN;
CREATE TABLE t_paper_balance_projections_new (
    c_space_id TEXT NOT NULL,
    c_trading_account_id TEXT NOT NULL,
    c_initialized_at INTEGER NOT NULL,
    PRIMARY KEY (c_space_id, c_trading_account_id),
    FOREIGN KEY (c_space_id, c_trading_account_id)
        REFERENCES t_paper_account_configs (c_space_id, c_trading_account_id),
    CHECK (c_initialized_at > 0)
);
INSERT INTO t_paper_balance_projections_new (c_space_id, c_trading_account_id, c_initialized_at)
SELECT c_space_id, c_trading_account_id, c_initialized_at FROM t_paper_balance_projections;
DROP TABLE t_paper_balance_projections;
ALTER TABLE t_paper_balance_projections_new RENAME TO t_paper_balance_projections;
DROP TABLE IF EXISTS t_replay_orders;
COMMIT;
PRAGMA foreign_keys = ON`
)

// 用单连接直接操作库文件（PRAGMA foreign_keys 按连接生效）。
func execOnFile(t *testing.T, path string, statements ...string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()
	for _, statement := range statements {
		require.NoError(t, db.Exec(statement).Error, statement)
	}
}

// 把新建的库改回旧结构：投影表带 c_applied_fill_count，并多一张 t_replay_orders。
func downgradeToLegacySchema(t *testing.T, path string) {
	t.Helper()
	execOnFile(t, path,
		"PRAGMA foreign_keys = OFF",
		legacyProjectionsDDL,
		"INSERT INTO t_paper_balance_projections_legacy (c_space_id, c_trading_account_id, c_applied_fill_count, c_initialized_at) SELECT c_space_id, c_trading_account_id, 3, c_initialized_at FROM t_paper_balance_projections",
		"DROP TABLE t_paper_balance_projections",
		"ALTER TABLE t_paper_balance_projections_legacy RENAME TO t_paper_balance_projections",
		legacyReplayOrdersDDL,
		"PRAGMA foreign_keys = ON",
	)
}

func TestOpenLegacyDatabaseNeedsManualDropBeforeStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	require.NoError(t, err)
	fill := seedPaperBalanceOrder(t, s)
	require.NoError(t, s.Close())
	downgradeToLegacySchema(t, path)

	// 旧库直接启动：表结构校验拒绝。
	rejected, err := Open(path)
	if rejected != nil {
		defer rejected.Close()
	}
	require.ErrorIs(t, err, ErrIncompatibleSchema)

	// 手工 SQL 之后启动：余额与投影行保留，下单、成交、撮合用到的余额读写都正常。
	execOnFile(t, path, manualDropLegacySQL)
	reopened, err := Open(path)
	require.NoError(t, err)
	defer reopened.Close()
	var columns int64
	require.NoError(t, reopened.db.Raw("SELECT COUNT(*) FROM pragma_table_info('t_paper_balance_projections') WHERE name = 'c_applied_fill_count'").Scan(&columns).Error)
	require.Zero(t, columns)
	var tables int64
	require.NoError(t, reopened.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name IN ('t_replay_orders', 'idx_replay_orders_period')").Scan(&tables).Error)
	require.Zero(t, tables)
	var violations []struct {
		Table string `gorm:"column:table"`
	}
	require.NoError(t, reopened.db.Raw("PRAGMA foreign_key_check").Scan(&violations).Error)
	require.Empty(t, violations)

	snapshot, err := reopened.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "100000", snapshot.Totals["USDT"].String())
	require.NoError(t, reopened.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
	snapshot, err = reopened.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "99899.9", snapshot.Totals["USDT"].String())
}

func TestAddPaperBalanceRejectsUninitializedAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uninitialized.db")
	s, err := Open(path)
	require.NoError(t, err)
	fill := seedPaperBalanceOrder(t, s)
	require.NoError(t, s.Close())
	// 清掉余额与投影，模拟账户还没有初始化余额。
	execOnFile(t, path,
		"PRAGMA foreign_keys = OFF",
		"DELETE FROM t_paper_asset_balances",
		"DELETE FROM t_paper_balance_projections",
		"PRAGMA foreign_keys = ON",
	)
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	err = s.Transaction(context.Background(), func(tx *Tx) error {
		return tx.addPaperBalance(fill.SpaceID, fill.TradingAccountID, "USDT", shared.MustDecimal("1"))
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
	require.ErrorContains(t, err, "uninitialized paper balance")

	// 已初始化的账户给还没有余额行的资产加余额：从零开始。
	s2 := openTestStore(t)
	fill2 := seedPaperBalanceOrder(t, s2)
	require.NoError(t, s2.Transaction(context.Background(), func(tx *Tx) error {
		return tx.addPaperBalance(fill2.SpaceID, fill2.TradingAccountID, "BNB", shared.MustDecimal("2"))
	}))
	snapshot, err := s2.GetPaperBalanceSnapshot(context.Background(), fill2.SpaceID, fill2.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "2", snapshot.Totals["BNB"].String())
}
