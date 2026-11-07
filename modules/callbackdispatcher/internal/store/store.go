// Package store 是事件中心自己的 SQLite 库：事件规则、事件回执、执行记录、通知尝试和下架处理步骤。
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	// ErrNotFound 表示记录不存在。
	ErrNotFound = errors.New("记录不存在")
	// ErrConflict 表示与现有记录冲突（例如同一标的已有未结束的下架处理）。
	ErrConflict = errors.New("记录冲突")
)

// Store 持有 SQLite 连接。
type Store struct {
	db *gorm.DB
}

// Open 打开（必要时创建）SQLite 库。库里有验证密钥，文件权限限制为仅当前用户。
func Open(dbPath string) (*Store, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, fmt.Errorf("数据库路径不能为空")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}
	// 不让 GORM 的日志把 SQL 参数（含验证密钥、事件内容）写进服务日志。
	db, err := gorm.Open(sqlite.Open(buildDSN(dbPath)), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// SQLite 单连接：写入排队，不会出现 database is locked。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := os.Chmod(dbPath, 0o600); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("限制数据库文件权限失败: %w", err)
	}
	return &Store{db: db}, nil
}

func buildDSN(dbPath string) string {
	pragmas := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(1)",
	}
	separator := "?"
	if strings.Contains(dbPath, "?") {
		separator = "&"
	}
	return dbPath + separator + strings.Join(pragmas, "&")
}

// ApplySchema 执行 schema（幂等）。
func (s *Store) ApplySchema(sql string) error {
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("schema 不能为空")
	}
	return s.db.Exec(sql).Error
}

// Ping 检查数据库是否可用。
func (s *Store) Ping(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec(pingSQL).Error
}

// Close 关闭连接。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

// 检查连接与表结构用到的查询。
const (
	pingSQL             = `SELECT 1`
	tableColumnsSQL     = `SELECT name FROM pragma_table_info(?)`
	delistActionsDDLSQL = `SELECT COALESCE(sql, '') FROM sqlite_master WHERE type = 'table' AND name = 't_delist_actions'`
)

// requiredColumns 是当前代码依赖的、后来加进去的列：schema 只有 CREATE TABLE IF NOT EXISTS，没有迁移，旧库文件缺这些列时运行时才会报 no such column。
var requiredColumns = map[string][]string{
	"t_event_executions": {"c_claimed_at", "c_cleanup_pending", "c_cleanup_seq"},
	"t_delist_actions":   {"c_busy_since", "c_next_reopen_seq", "c_planned_delist_at", "c_release_verdict", "c_reopen_seq", "c_self_released"},
}

// CheckSchema 检查库里的表结构是不是当前版本：缺列，或者 t_delist_actions 的状态约束里没有 PARTIAL，说明是旧库。
// 旧库不检查的后果很隐蔽：写步骤一直失败，记录每分钟被放回，要 15 分钟后才以“连续暂时失败”告警，原因也不直观。事件中心没有迁移，旧库必须删掉重建，这里直接拒绝启动。
func (s *Store) CheckSchema(ctx context.Context) error {
	var problems []string
	tables := make([]string, 0, len(requiredColumns))
	for table := range requiredColumns {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		var columns []string
		if err := s.db.WithContext(ctx).Raw(tableColumnsSQL, table).Scan(&columns).Error; err != nil {
			return fmt.Errorf("读取表 %s 的结构失败: %w", table, err)
		}
		for _, column := range requiredColumns[table] {
			if !slices.Contains(columns, column) {
				problems = append(problems, table+" 缺少列 "+column)
			}
		}
	}
	var ddl string
	if err := s.db.WithContext(ctx).Raw(delistActionsDDLSQL).Scan(&ddl).Error; err != nil {
		return fmt.Errorf("读取 t_delist_actions 的定义失败: %w", err)
	}
	if !strings.Contains(ddl, "'PARTIAL'") {
		problems = append(problems, "t_delist_actions 的状态约束里没有 PARTIAL")
	}
	if len(problems) > 0 {
		return fmt.Errorf("事件中心的数据库是旧版本的结构（%s）；事件中心没有数据库迁移，请先停止服务、删除库文件后再启动", strings.Join(problems, "；"))
	}
	return nil
}
