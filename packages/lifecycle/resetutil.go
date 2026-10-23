package lifecycle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 重置用的 SQLite 小工具：备份、列出业务表、清表。各模块的存储层用同一份实现。

var identPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ResetBackupDir 返回本次重置的备份目录 <baseDir>/reset-backups/<operationID>（权限 0700），并只保留最近 keep 份。
func ResetBackupDir(baseDir, operationID string, keep int) (string, error) {
	root := filepath.Join(baseDir, "reset-backups")
	dir := filepath.Join(root, operationID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if keep > 0 {
		entries, err := os.ReadDir(root)
		if err == nil {
			var names []string
			for _, e := range entries {
				if e.IsDir() {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names) // operation_id 以时间开头，字典序即时间序
			for len(names) > keep {
				_ = os.RemoveAll(filepath.Join(root, names[0]))
				names = names[1:]
			}
		}
	}
	return dir, nil
}

// driverConnector 用应用已经在用的驱动按 DSN 另开连接，不需要自己注册驱动（避免和各模块导入的 sqlite 驱动重复注册）。
type driverConnector struct {
	drv driver.Driver
	dsn string
}

func (c driverConnector) Connect(context.Context) (driver.Conn, error) { return c.drv.Open(c.dsn) }
func (c driverConnector) Driver() driver.Driver                        { return c.drv }

var sqliteFiles sync.Map // *sql.DB → 库文件路径

// RegisterSQLiteFile 登记应用库连接对应的文件路径。重置的长耗时操作要另开连接（见 dedicated），
// 而应用连接可能正被长查询占着，没法再向它查询文件路径，所以由模块在拿到连接时登记。
func RegisterSQLiteFile(db *sql.DB, path string) {
	if db != nil && path != "" {
		sqliteFiles.Store(db, path)
	}
}

// dedicated 为重置的长耗时操作（备份、清表、统计）另开一条连接。
// 应用的库几乎都把连接池限制为 1 条：备份一个几个 GB 的库会占满这唯一的连接，健康检查的 SELECT 1 排不上队，
// 就绪探测失败、守护进程把正在重置的模块重启了（线上策略库 1.8 GB 就这样被中断）。
// SQLite 是 WAL 模式，读写互不阻塞，另开连接不影响应用；内存库或读不到文件路径时退回用原连接。
func dedicated(ctx context.Context, db *sql.DB) (*sql.DB, func()) {
	file := ""
	if v, ok := sqliteFiles.Load(db); ok {
		file = v.(string)
	} else {
		// 没有登记时向应用连接查询路径；它被占着就最多等 3 秒，退回用原连接。
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if rows, err := db.QueryContext(probe, "PRAGMA database_list"); err == nil {
			for rows.Next() {
				var seq int
				var name, path string
				if err := rows.Scan(&seq, &name, &path); err == nil && name == "main" {
					file = path
				}
			}
			_ = rows.Close()
		}
	}
	if file == "" {
		return db, func() {}
	}
	conn := sql.OpenDB(driverConnector{drv: db.Driver(), dsn: file + "?_pragma=busy_timeout(120000)&_pragma=foreign_keys(ON)"})
	conn.SetMaxOpenConns(1)
	return conn, func() { _ = conn.Close() }
}

// SQLiteBackup 用 VACUUM INTO 把数据库完整备份到 dir/name（一致性快照，可直接作为库文件使用）。
func SQLiteBackup(ctx context.Context, db *sql.DB, dir, name string) (string, error) {
	path := filepath.Join(dir, name)
	_ = os.Remove(path) // 续做时重新备份：VACUUM INTO 要求目标不存在
	db, release := dedicated(ctx, db)
	defer release()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return "", fmt.Errorf("备份数据库到 %s：%w", path, err)
	}
	return path, nil
}

// BusinessTables 列出库里的业务表（t_ 开头）。
func BusinessTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 't\\_%' ESCAPE '\\' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// TableRows 统计各表行数（只用于计划展示）。
func TableRows(ctx context.Context, db *sql.DB, tables []string) (map[string]int64, error) {
	out := make(map[string]int64, len(tables))
	db, release := dedicated(ctx, db)
	defer release()
	for _, t := range tables {
		if !identPattern.MatchString(t) {
			return nil, fmt.Errorf("非法表名 %q", t)
		}
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t).Scan(&n); err != nil {
			return nil, fmt.Errorf("统计 %s：%w", t, err)
		}
		out[t] = n
	}
	return out, nil
}

// ClearTables 在一个事务里清空这些表（推迟外键检查，所以顺序无关）并重置自增序列；表结构保留。返回各表删除的行数。
func ClearTables(ctx context.Context, db *sql.DB, tables []string) (map[string]int64, error) {
	for _, t := range tables {
		if !identPattern.MatchString(t) {
			return nil, fmt.Errorf("非法表名 %q", t)
		}
	}
	db, release := dedicated(ctx, db)
	defer release()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return nil, err
	}
	deleted := make(map[string]int64, len(tables))
	for _, t := range tables {
		res, err := tx.ExecContext(ctx, "DELETE FROM "+t)
		if err != nil {
			return nil, fmt.Errorf("清空 %s：%w", t, err)
		}
		deleted[t], _ = res.RowsAffected()
		// 自增序列表只在有 AUTOINCREMENT 列时存在；表不存在时忽略错误。
		_, _ = tx.ExecContext(ctx, "DELETE FROM sqlite_sequence WHERE name = ?", t)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deleted, nil
}

// ClassifyTables 校验业务表都已归类：库里的每张业务表必须出现在 groups 的某一组里，
// 每个登记的表也必须真实存在。返回未归类的表（模块的 Plan 把它们作为阻止项，测试用它防止 schema 漂移）。
func ClassifyTables(actual []string, groups ...[]string) (unclassified, missing []string) {
	known := map[string]bool{}
	for _, g := range groups {
		for _, t := range g {
			known[t] = true
		}
	}
	have := map[string]bool{}
	for _, t := range actual {
		have[t] = true
		if !known[t] {
			unclassified = append(unclassified, t)
		}
	}
	for t := range known {
		if !have[t] {
			missing = append(missing, t)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(missing)
	return unclassified, missing
}

// JoinTables 把表名拼成展示用的字符串。
func JoinTables(tables []string) string { return strings.Join(tables, "、") }
