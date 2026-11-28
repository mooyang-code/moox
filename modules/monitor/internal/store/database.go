// Package store 负责 Monitor 的 SQLite 连接与仓储持久化。
package store

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/sqlitedb"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// gormLogger 保留 GORM 的慢查询与出错日志，忽略常规未找到错误并关闭终端颜色。
// ParameterizedQueries 让日志里的 SQL 只留占位符：通知渠道的 Webhook 地址和密钥是推送凭据，不能随语句写进日志。
var gormLogger = logger.New(log.New(os.Stdout, "", log.LstdFlags), logger.Config{
	SlowThreshold:             200 * time.Millisecond,
	LogLevel:                  logger.Warn,
	IgnoreRecordNotFoundError: true,
	ParameterizedQueries:      true,
})

type Store struct {
	db   *gorm.DB
	path string
}

func Open(path string) (*Store, error) {
	return openWithOptions(sqlitedb.Options{
		Path:            path,
		MaxOpenConns:    1,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Hour,
		Logger:          gormLogger,
	})
}

func OpenFromConfig(cfg config.DatabaseConfig) (*Store, error) {
	lifetime := cfg.ConnMaxLifetime
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	return openWithOptions(sqlitedb.Options{
		Path:            cfg.Path,
		MaxOpenConns:    cfg.MaxOpenConns,
		MaxIdleConns:    cfg.MaxIdleConns,
		ConnMaxLifetime: lifetime,
		ConnMaxIdleTime: cfg.ConnMaxIdleTime,
		Logger:          gormLogger,
	})
}

func openWithOptions(opts sqlitedb.Options) (*Store, error) {
	db, err := sqlitedb.OpenGORM(opts)
	if err != nil {
		return nil, fmt.Errorf("打开监控数据库失败：%w", err)
	}
	return &Store{db: db, path: opts.Path}, nil
}

// Repositories builds the monitor control-plane repository graph.
func (m *Store) Repositories() *Repositories {
	if m == nil {
		return nil
	}
	return NewRepositories(m.db)
}

// WithDatabase runs a short-lived construction callback against the private
// database handle. Production callers should prefer Repositories or domain
// methods; this escape hatch is reserved for bounded-context adapters.
func WithDatabase[T any](m *Store, fn func(*gorm.DB) T) (T, error) {
	var zero T
	if m == nil || m.db == nil {
		return zero, fmt.Errorf("monitor database is not open")
	}
	if fn == nil {
		return zero, fmt.Errorf("database callback is nil")
	}
	return fn(m.db), nil
}

// Ping verifies that the database is available.
func (m *Store) Ping(ctx context.Context) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("monitor database is not open")
	}
	return m.db.WithContext(ctx).Exec("SELECT 1").Error
}

func (m *Store) ApplySchema(sqlText string) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("监控数据库未打开")
	}
	return sqlitedb.EnsureSchemaGORM(m.db, m.path, "monitor", sqlText, dataformat.LifecycleParts(), lifecycle.StateFileForDB(m.path))
}

func (m *Store) Close() error {
	if m == nil || m.db == nil {
		return nil
	}
	sqlDB, err := m.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
