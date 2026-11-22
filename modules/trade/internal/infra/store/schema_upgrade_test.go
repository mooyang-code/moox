package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// upgradeOrderRetentionSQL 是给加列之前的线上库补订单保留天数的升级语句：停 Trade 后执行，再换二进制启动。
// SQLite 把新列接在最后一个字段之后、表级约束之前，与 paper_account_config.sql 里的写法逐字一致，启动检查才能通过；
// 小时汇总表由启动时的 CREATE TABLE IF NOT EXISTS 建出来。
const upgradeOrderRetentionSQL = `ALTER TABLE t_paper_account_configs ADD COLUMN c_order_retention_days INTEGER NOT NULL DEFAULT 5;`

// 旧库（模拟配置没有保留天数列、没有小时汇总表）执行升级语句后，新代码能打开，重开一次也能通过启动检查，旧行读出来是 5 天。
// 不执行升级语句时启动检查拒绝旧库。
func TestOldStoreOpensAfterOrderRetentionUpgrade(t *testing.T) {
	const retentionColumn = "    c_order_retention_days INTEGER NOT NULL DEFAULT 5,\n"
	require.Equal(t, 1, strings.Count(schema.AllSQL(), retentionColumn), "模拟旧库要恰好去掉保留天数这一列")
	oldSQL := strings.Replace(schema.AllSQL(), retentionColumn, "", 1)

	path := filepath.Join(t.TempDir(), "trade.db")
	old, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, old.Exec(oldSQL).Error)
	require.NoError(t, old.Exec(`DROP TABLE t_order_fill_hourly_summaries`).Error)
	require.NoError(t, old.Exec(`INSERT INTO t_trading_accounts (
			c_space_id, c_trading_account_id, c_name, c_exchange, c_markets, c_execution_mode,
			c_credential_secret_id, c_settlement_asset, c_status
		) VALUES ('space-1', 'paper-1', 'paper', 'BINANCE', 'SPOT', 'PAPER', '', 'USDT', 'ENABLED')`).Error)
	require.NoError(t, old.Exec(`INSERT INTO t_paper_account_configs (
			c_space_id, c_trading_account_id, c_initial_balance, c_maker_fee_rate, c_taker_fee_rate, c_slippage_bps
		) VALUES ('space-1', 'paper-1', '100000', '0.0002', '0.0005', '1')`).Error)
	closeGorm(t, old)

	_, err = Open(path)
	require.ErrorIs(t, err, ErrIncompatibleSchema, "没执行升级语句的旧库要被启动检查拒绝")

	upgraded, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, upgraded.Exec(upgradeOrderRetentionSQL).Error)
	closeGorm(t, upgraded)

	for round := 0; round < 2; round++ {
		s, err := Open(path)
		require.NoError(t, err, "第 %d 次打开", round+1)
		config, err := s.GetPaperAccountConfig(context.Background(), "space-1", "paper-1")
		require.NoError(t, err)
		require.Equal(t, 5, config.OrderRetentionDays)
		require.Equal(t, "0.0005", config.TakerFeeRate)
		require.NoError(t, s.Close())
	}
}

func closeGorm(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}
