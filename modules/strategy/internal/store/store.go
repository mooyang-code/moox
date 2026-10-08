// Package store 是策略模块的 SQLite 持久化层：定义、实例、会话、结果与解释、回放。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// ErrNotFound 表示记录不存在。
var ErrNotFound = gorm.ErrRecordNotFound

// Store 持有单写连接的 SQLite 数据库。
type Store struct {
	db *gorm.DB
}

// New 用已打开的连接构造 Store（测试使用）。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// Open 打开策略数据库并配置单写连接池；已存在的表必须与当前 schema 一致。
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("策略数据库路径不能为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建策略数据库目录失败：%w", err)
	}
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("打开策略数据库失败：%w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取策略数据库连接失败：%w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	store := New(db)
	if err := store.validateExistingSchema(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return store, nil
}

// Ping 检查数据库是否可用。
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("策略数据库未打开")
	}
	return s.db.WithContext(ctx).Exec("SELECT 1").Error
}

// ApplySchema 执行建表 SQL 并校验结果。
func (s *Store) ApplySchema(sql string) error {
	if s == nil || s.db == nil {
		return errors.New("策略数据库未打开")
	}
	if strings.TrimSpace(sql) == "" {
		return errors.New("策略 schema 为空")
	}
	if err := s.db.Exec(sql).Error; err != nil {
		return err
	}
	return s.validateCurrentSchema()
}

// Close 释放数据库连接。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Transaction 在一个事务内执行 fn（供需要跨表原子性的调用方使用）。
func (s *Store) transaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(fn)
}

var schemaColumns = map[string][]string{
	"t_strategy_defs":         {"c_strategy_id", "c_name", "c_dsl_yaml", "c_dsl_hash", "c_deleted_at", "c_ctime", "c_mtime"},
	"t_strategy_def_versions": {"c_dsl_hash", "c_dsl_yaml", "c_ctime"},
	"t_strategy_instances":    {"c_instance_id", "c_strategy_id", "c_space_id", "c_view_id", "c_logical_account_id", "c_enabled", "c_session_id", "c_resolved_json", "c_health", "c_deleted_at", "c_ctime", "c_mtime"},
	"t_strategy_sessions":     {"c_session_id", "c_instance_id", "c_dsl_hash", "c_resolved_json", "c_ctime", "c_closed_at"},
	"t_strategy_results":      {"c_result_id", "c_instance_id", "c_session_id", "c_bar_end_time", "c_valid_until", "c_status", "c_skip_reason", "c_dsl_hash", "c_input_json", "c_targets_json", "c_rule_states_json", "c_summary_json", "c_event_data", "c_publish_status", "c_ctime"},
	"t_strategy_result_items": {"c_result_id", "c_rule_id", "c_instrument_id", "c_stage", "c_score", "c_rank", "c_weight", "c_reason"},
	"t_strategy_replays":      {"c_replay_id", "c_strategy_id", "c_dsl_yaml", "c_space_id", "c_view_id", "c_start_time", "c_end_time", "c_fee_bps", "c_status", "c_progress_time", "c_metrics_json", "c_error", "c_ctime", "c_mtime"},
	"t_strategy_replay_bars":  {"c_replay_id", "c_bar_end_time", "c_status", "c_targets_json", "c_positions_json", "c_summary_json", "c_return", "c_equity", "c_turnover", "c_fee"},
}

// requiredIndexes 是必须存在的索引及其列；校验时还比对唯一性与是否为部分索引。
var requiredIndexes = []struct {
	table   string
	name    string
	columns string
	unique  bool
	partial bool
}{
	{"t_strategy_instances", "ux_t_strategy_instances_account", "c_space_id\x00c_logical_account_id", true, true},
	{"t_strategy_instances", "idx_t_strategy_instances_view", "c_space_id\x00c_view_id\x00c_enabled", false, false},
	{"t_strategy_sessions", "idx_t_strategy_sessions_instance", "c_instance_id\x00c_ctime", false, false},
	{"t_strategy_results", "idx_t_strategy_results_instance_bar", "c_instance_id\x00c_bar_end_time", false, false},
	{"t_strategy_results", "idx_t_strategy_results_latest_ok", "c_instance_id\x00c_session_id\x00c_bar_end_time", false, true},
	{"t_strategy_results", "idx_t_strategy_results_pending", "c_ctime\x00c_result_id", false, true},
	{"t_strategy_replays", "idx_t_strategy_replays_space", "c_space_id\x00c_ctime", false, false},
}

func (s *Store) validateExistingSchema() error {
	tables, err := s.strategyTables()
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}
	return s.validateSchemaTables(tables)
}

func (s *Store) validateCurrentSchema() error {
	tables, err := s.strategyTables()
	if err != nil {
		return err
	}
	return s.validateSchemaTables(tables)
}

func (s *Store) strategyTables() ([]string, error) {
	var tables []string
	if err := s.db.Raw(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND (name = 't_strategies' OR name LIKE 't_strategy_%')
		ORDER BY name
	`).Scan(&tables).Error; err != nil {
		return nil, fmt.Errorf("读取策略数据库表失败：%w", err)
	}
	return tables, nil
}

func (s *Store) validateSchemaTables(tables []string) error {
	for _, table := range tables {
		if _, ok := schemaColumns[table]; !ok {
			return obsoleteSchemaError(table)
		}
	}
	if len(tables) != len(schemaColumns) {
		table := "Strategy"
		if len(tables) > 0 {
			table = tables[0]
		}
		return obsoleteSchemaError(table)
	}
	for _, table := range tables {
		var columns []string
		if err := s.db.Raw(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table).Scan(&columns).Error; err != nil {
			return fmt.Errorf("读取表 %s 的列失败：%w", table, err)
		}
		if strings.Join(columns, "\x00") != strings.Join(schemaColumns[table], "\x00") {
			return obsoleteSchemaError(table)
		}
	}
	for _, index := range requiredIndexes {
		var info struct {
			Name    string `gorm:"column:name"`
			Unique  int    `gorm:"column:unique"`
			Partial int    `gorm:"column:partial"`
		}
		if err := s.db.Raw(`SELECT name, [unique], partial FROM pragma_index_list(?) WHERE name = ?`, index.table, index.name).Scan(&info).Error; err != nil {
			return fmt.Errorf("读取索引 %s 失败：%w", index.name, err)
		}
		if info.Name != index.name || (info.Unique == 1) != index.unique || (info.Partial == 1) != index.partial {
			return obsoleteSchemaError(index.table)
		}
		var columns []string
		if err := s.db.Raw(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index.name).Scan(&columns).Error; err != nil {
			return fmt.Errorf("读取索引 %s 的列失败：%w", index.name, err)
		}
		if strings.Join(columns, "\x00") != index.columns {
			return obsoleteSchemaError(index.table)
		}
	}
	return nil
}

func obsoleteSchemaError(table string) error {
	return fmt.Errorf("Strategy 数据库表 %s 使用旧 schema；请先停止消费者并备份数据库，再人工选择归档旧库或重建当前 schema", table)
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

func stringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time.UTC()
	return &at
}

func timeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func millis(at time.Time) int64 { return at.UTC().UnixMilli() }

func fromMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func requireTime(at time.Time) (time.Time, error) {
	if at.IsZero() {
		return time.Time{}, errors.New("时间不能为空")
	}
	return at.UTC(), nil
}
