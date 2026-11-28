// Package store 是策略模块的 SQLite 持久化层：定义、实例、会话、结果与解释、回放。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	sqlitedriver "github.com/glebarez/go-sqlite"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/sqlitedb"
	"gorm.io/gorm"
)

// ErrNotFound 表示记录不存在。
var ErrNotFound = gorm.ErrRecordNotFound

// sqliteConstraint 是 SQLite 约束错误的主错误码。
const sqliteConstraint = 19

// FriendlyMessage 把错误链中的 SQLite 驱动错误替换为中文概述，避免把表名、列名等内部细节返回给接口调用方；
// replaced 为 true 时调用方应把原始错误写入日志。
func FriendlyMessage(err error) (message string, replaced bool) {
	// 取消、超时与数据库关闭只替换错误链里的英文原文，保留前面的中文上下文（例如“实例已停用，但 Trade 未确认关闭会话”）。
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
	db   *gorm.DB
	path string
	// onCancel 在待投递结果被取消、事务提交之后调用，用于按原因计数。
	onCancel func(reason string, count int64)
	// onSentAfterCancel 在已取消的结果确认已经发出、改记 sent 之后调用：取消计数只增不减，这类结果另行计数。
	onSentAfterCancel func()
}

// 待投递结果被取消的原因。
const (
	CancelSuperseded = "superseded" // 被同一会话更新的 ok 结果替代
	CancelExpired    = "expired"    // 投递前已过有效期
	CancelInactive   = "inactive"   // 投递前实例已停用、删除或换了会话
	CancelRejected   = "rejected"   // 事件无法通过契约校验（永久发布错误）
)

// CancelReasons 是全部取消原因。
var CancelReasons = []string{CancelSuperseded, CancelExpired, CancelInactive, CancelRejected}

// SetCancelObserver 设置待投递结果被取消时的回调（事务提交之后调用）；传 nil 关闭。
func (s *Store) SetCancelObserver(fn func(reason string, count int64)) { s.onCancel = fn }

// SetSentAfterCancelObserver 设置已取消的结果确认已经发出、改记 sent 时的回调；传 nil 关闭。
func (s *Store) SetSentAfterCancelObserver(fn func()) { s.onSentAfterCancel = fn }

func (s *Store) cancelled(reason string, count int64) {
	if count > 0 && s.onCancel != nil {
		s.onCancel(reason, count)
	}
}

// New 用已打开的连接构造 Store（测试使用）。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// DBForTest 返回底层连接（仅测试使用，例如切换 PRAGMA query_only 模拟只读故障）。
func (s *Store) DBForTest() *gorm.DB { return s.db }

// Open 打开策略数据库并配置单写连接池；已存在的表必须与当前 schema 一致。
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("策略数据库路径不能为空")
	}
	db, err := sqlitedb.OpenGORM(sqlitedb.Options{Path: path})
	if err != nil {
		return nil, fmt.Errorf("打开策略数据库失败：%w", err)
	}
	if err := sqlitedb.ValidateGORM(db, "strategy", schema.AllSQL(), true); err != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

// 连接检查用的 SQL：整条写成常量，不在调用处拼接。建表 SQL 来自 schema 包，不在这里。
const (
	// sqlPing 检查连接可用。
	sqlPing = "SELECT 1"
)

// Ping 检查数据库是否可用。
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("策略数据库未打开")
	}
	return s.db.WithContext(ctx).Exec(sqlPing).Error
}

// ApplySchema 先核对已有对象与数据格式文件，再执行建表 SQL、严格校验 schema，并在通过后写入数据格式文件。
func (s *Store) ApplySchema(sqlText string) error {
	if s == nil || s.db == nil {
		return errors.New("策略数据库未打开")
	}
	if strings.TrimSpace(sqlText) == "" {
		return errors.New("策略 schema 为空")
	}
	return sqlitedb.EnsureSchemaGORM(s.db, s.path, "strategy", sqlText, dataformat.LifecycleParts(), lifecycle.StateFileForDB(s.path))
}

// ExecForTest 直接执行原始 SQL（仅测试里注入故障触发器或构造异常库状态使用，不走 schema 校验）。
func (s *Store) ExecForTest(sqlText string) error {
	if s == nil || s.db == nil {
		return errors.New("策略数据库未打开")
	}
	return s.db.Exec(sqlText).Error
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
