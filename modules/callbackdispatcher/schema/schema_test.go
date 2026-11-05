package schema

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openMemory(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.Exec("PRAGMA foreign_keys = ON").Error)
	require.NoError(t, db.Exec(AllSQL()).Error)
	// 重复执行必须幂等。
	require.NoError(t, db.Exec(AllSQL()).Error)
	return db
}

func TestAllSQLCreatesTables(t *testing.T) {
	db := openMemory(t)
	var tables []string
	require.NoError(t, db.Raw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`).Scan(&tables).Error)
	require.Equal(t, []string{"t_delist_actions", "t_event_attempts", "t_event_executions", "t_event_receipts", "t_event_rules"}, tables)
}

func insertExecution(db *gorm.DB, id, dedupe, status string) error {
	return db.Exec(`INSERT INTO t_event_executions
		(c_execution_id, c_rule_id, c_rule_name, c_action_type, c_event_id, c_event_name, c_space_id, c_subject_id, c_occurred_at, c_payload_json, c_status, c_dedupe_key, c_created_at, c_updated_at)
		VALUES (?, 'rule-1', '规则', 'SYSTEM', ?, 'event.collector.instrument.delist_scheduled', 'mooxsys', 'binance:SPOT:FOOUSDT', 1, '{}', ?, ?, 1, 1)`,
		id, "event-"+id, status, dedupe).Error
}

func TestDedupeKeyUniqueOnlyAmongUnfinishedExecutions(t *testing.T) {
	db := openMemory(t)
	require.NoError(t, insertExecution(db, "e1", "rule-1|binance:SPOT:FOOUSDT", "PENDING"))
	require.Error(t, insertExecution(db, "e2", "rule-1|binance:SPOT:FOOUSDT", "PENDING"))
	require.NoError(t, db.Exec(`UPDATE t_event_executions SET c_status = 'CANCELED' WHERE c_execution_id = 'e1'`).Error)
	require.NoError(t, insertExecution(db, "e2", "rule-1|binance:SPOT:FOOUSDT", "PENDING"))
	// 通知地址的记录没有合并键，可以有多条。
	require.NoError(t, insertExecution(db, "e3", "", "PENDING"))
	require.NoError(t, insertExecution(db, "e4", "", "PENDING"))
}

func TestRuleConstraints(t *testing.T) {
	db := openMemory(t)
	insert := func(id, action, url, secret, system string, isSystem int) error {
		return db.Exec(`INSERT INTO t_event_rules (c_rule_id, c_name, c_event_name, c_action_type, c_notify_url, c_secret, c_system_action, c_system, c_created_at, c_updated_at)
			VALUES (?, '规则', 'event.collector.instrument.delist_scheduled', ?, ?, ?, ?, ?, 1, 1)`, id, action, url, secret, system, isSystem).Error
	}
	require.NoError(t, insert("r1", "NOTIFY_URL", "https://example.com/hook", "secret", "", 0))
	require.Error(t, insert("r2", "NOTIFY_URL", "", "secret", "", 0), "通知地址规则必须有地址")
	require.Error(t, insert("r3", "NOTIFY_URL", "https://example.com/hook", "", "", 0), "通知地址规则必须有密钥")
	require.NoError(t, insert("r4", "SYSTEM", "", "", "SELL_DELISTED_HOLDINGS", 1))
	require.Error(t, insert("r5", "SYSTEM", "", "", "SELL_DELISTED_HOLDINGS", 1), "每种系统动作只有一条")
	require.Error(t, insert("r6", "SYSTEM", "https://example.com", "", "SELL_DELISTED_HOLDINGS", 1))
}

func TestDeletingExecutionRemovesAttempts(t *testing.T) {
	db := openMemory(t)
	require.NoError(t, insertExecution(db, "e1", "", "PENDING"))
	require.NoError(t, db.Exec(`INSERT INTO t_event_attempts (c_execution_id, c_attempt, c_at) VALUES ('e1', 1, 1)`).Error)
	require.NoError(t, db.Exec(`DELETE FROM t_event_executions WHERE c_execution_id = 'e1'`).Error)
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM t_event_attempts`).Scan(&count).Error)
	require.Zero(t, count)
}
