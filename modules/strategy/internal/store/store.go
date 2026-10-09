// Package store 是策略模块的 SQLite 持久化层：定义、实例、会话、结果与解释、回放。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	sqlitedriver "github.com/glebarez/go-sqlite"
	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

// ErrNotFound 表示记录不存在。
var ErrNotFound = gorm.ErrRecordNotFound

// sqliteConstraint 是 SQLite 约束错误的主错误码。
const sqliteConstraint = 19

// FriendlyMessage 把错误链中的 SQLite 驱动错误替换为中文概述，避免把表名、列名等内部细节返回给接口调用方；
// replaced 为 true 时调用方应把原始错误写入日志。
func FriendlyMessage(err error) (message string, replaced bool) {
	// 取消、超时与数据库关闭只替换错误链里的英文原文，保留前面的中文上下文（例如“已停用但 Trade 释放未确认”）。
	for _, known := range []struct {
		match    bool
		raw      []string
		friendly string
	}{
		{errors.Is(err, context.Canceled), []string{context.Canceled.Error()}, "请求已取消"},
		{errors.Is(err, context.DeadlineExceeded), []string{context.DeadlineExceeded.Error()}, "请求超时"},
		{errors.Is(err, sql.ErrConnDone) || strings.Contains(err.Error(), "sql: database is closed"), []string{sql.ErrConnDone.Error(), "sql: database is closed"}, "策略数据库不可用（进程正在关闭）"},
	} {
		if !known.match {
			continue
		}
		full := err.Error()
		for _, raw := range known.raw {
			if strings.Contains(full, raw) {
				return strings.Replace(full, raw, known.friendly, 1), true
			}
		}
		// 错误链里的超时或取消已经由上一层换成了中文说明（例如 Trade 请求超时、结果未知），原样保留，不能整句覆盖。
		if containsHan(full) {
			return full, false
		}
		return known.friendly + "，请稍后重试", true
	}
	if errors.Is(err, ErrNotFound) {
		full := err.Error()
		if raw := ErrNotFound.Error(); strings.Contains(full, raw) {
			return strings.Replace(full, raw, "记录不存在", 1), true
		}
		return "记录不存在", true
	}
	var sqliteErr *sqlitedriver.Error
	if !errors.As(err, &sqliteErr) {
		return err.Error(), false
	}
	friendly := "策略数据库操作失败"
	switch sqliteErr.Code() {
	case 2067, 1555: // SQLITE_CONSTRAINT_UNIQUE、SQLITE_CONSTRAINT_PRIMARYKEY
		friendly = "记录已存在（标识重复）"
	case 787: // SQLITE_CONSTRAINT_FOREIGNKEY
		friendly = "引用的记录不存在"
	}
	full := err.Error()
	if raw := sqliteErr.Error(); raw != "" && strings.Contains(full, raw) {
		return strings.Replace(full, raw, friendly, 1), true
	}
	return friendly, true
}

// containsHan 报告文本里是否有汉字。
func containsHan(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// IsPermanentWriteError 判断写入错误是否是确定性的：结果校验失败或违反表约束，原样重试不会成功。
func IsPermanentWriteError(err error) bool {
	if errors.Is(err, ErrResultInvalid) {
		return true
	}
	var sqliteErr *sqlitedriver.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqliteConstraint
}

// Store 持有单写连接的 SQLite 数据库。
type Store struct {
	db *gorm.DB
	// onCancel 在待投递结果被取消、事务提交之后调用，用于按原因计数。
	onCancel func(reason string, count int64)
}

// 待投递结果被取消的原因。
const (
	CancelSuperseded = "superseded" // 被同一会话更新的 ok 结果替代
	CancelExpired    = "expired"    // 投递前已过有效期
	CancelInactive   = "inactive"   // 投递前实例已停用、删除或换了会话
	CancelRejected   = "rejected"   // 事件无法通过契约校验（永久发布错误）
)

// SetCancelObserver 设置待投递结果被取消时的回调（事务提交之后调用）；传 nil 关闭。
func (s *Store) SetCancelObserver(fn func(reason string, count int64)) { s.onCancel = fn }

func (s *Store) cancelled(reason string, count int64) {
	if count > 0 && s.onCancel != nil {
		s.onCancel(reason, count)
	}
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
	// 多条建表语句放在一个事务里：首次启动中途崩溃不会留下残缺的 schema（残缺的库下次启动会被拒绝）。
	if err := s.db.Transaction(func(tx *gorm.DB) error { return tx.Exec(sql).Error }); err != nil {
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

// schemaObject 是 sqlite_master 里的一个表或索引；SQL 已规范化。
type schemaObject struct {
	Type  string `gorm:"column:type"`
	Name  string `gorm:"column:name"`
	Table string `gorm:"column:tbl_name"`
	SQL   string `gorm:"column:sql"`
}

var expectedSchema struct {
	once    sync.Once
	objects map[string]schemaObject
	err     error
}

// expectedSchemaObjects 把当前 schema 载入一个内存库，读出它在 sqlite_master 里的表与索引，作为比对基准。
func expectedSchemaObjects() (map[string]schemaObject, error) {
	expectedSchema.once.Do(func() {
		db, err := gorm.Open(sqlite.Open("file:strategy-schema?mode=memory"), &gorm.Config{})
		if err != nil {
			expectedSchema.err = err
			return
		}
		sqlDB, err := db.DB()
		if err != nil {
			expectedSchema.err = err
			return
		}
		// 私有内存库只在建立它的连接上可见：固定单连接，建表与读取用的是同一个库。
		sqlDB.SetMaxOpenConns(1)
		defer func() { _ = sqlDB.Close() }()
		if err := db.Exec(schema.AllSQL()).Error; err != nil {
			expectedSchema.err = fmt.Errorf("载入策略 schema 失败：%w", err)
			return
		}
		expectedSchema.objects, expectedSchema.err = readSchemaObjects(db)
	})
	return expectedSchema.objects, expectedSchema.err
}

// readSchemaObjects 读出策略表及其显式索引（自动索引没有 SQL，随表定义一起比较）。
func readSchemaObjects(db *gorm.DB) (map[string]schemaObject, error) {
	var objects []schemaObject
	if err := db.Raw(`
		SELECT type, name, tbl_name, sql FROM sqlite_master
		WHERE type IN ('table', 'index') AND sql IS NOT NULL AND (tbl_name = 't_strategies' OR tbl_name LIKE 't_strategy_%')
	`).Scan(&objects).Error; err != nil {
		return nil, fmt.Errorf("读取策略数据库表失败：%w", err)
	}
	byKey := make(map[string]schemaObject, len(objects))
	for _, object := range objects {
		object.SQL = normalizeSchemaSQL(object.SQL)
		byKey[object.Type+"\x00"+object.Name] = object
	}
	return byKey, nil
}

// normalizeSchemaSQL 规范化建表语句以便比较：去掉 -- 注释，引号外的空白全部去掉、字母统一小写，
// 只按 AGENTS.md 调整排版（换行、缩进、括号与逗号两侧的空格、关键字大小写、注释）不影响结果。
func normalizeSchemaSQL(text string) string {
	var out strings.Builder
	quote := byte(0)
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			out.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '\'' || c == '"':
			quote = c
			out.WriteByte(c)
		case c == '-' && i+1 < len(text) && text[i+1] == '-':
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		case c >= 'A' && c <= 'Z':
			out.WriteByte(c + ('a' - 'A'))
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// validateSchema 校验策略表与当前 schema 一致：表的集合相同，每个表与索引的定义（列、约束、索引列与条件）逐字等价。
// 没有任何策略表的新库在 allowEmpty 时直接通过（随后由 ApplySchema 建表）。额外的触发器与索引不影响判断。
func (s *Store) validateSchema(allowEmpty bool) error {
	expected, err := expectedSchemaObjects()
	if err != nil {
		return err
	}
	actual, err := readSchemaObjects(s.db)
	if err != nil {
		return err
	}
	tables := 0
	for _, object := range actual {
		if object.Type != "table" {
			continue
		}
		tables++
		if _, ok := expected["table\x00"+object.Name]; !ok {
			return obsoleteSchemaError(object.Name)
		}
	}
	if tables == 0 && allowEmpty {
		return nil
	}
	keys := make([]string, 0, len(expected))
	for key := range expected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		want := expected[key]
		if got, ok := actual[key]; !ok || got.SQL != want.SQL {
			return obsoleteSchemaError(want.Table)
		}
	}
	return nil
}

func (s *Store) validateExistingSchema() error { return s.validateSchema(true) }

func (s *Store) validateCurrentSchema() error { return s.validateSchema(false) }

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
