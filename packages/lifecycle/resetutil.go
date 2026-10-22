package lifecycle

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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

// SQLiteBackup 用 VACUUM INTO 把数据库完整备份到 dir/name（一致性快照，可直接作为库文件使用）。
func SQLiteBackup(ctx context.Context, db *sql.DB, dir, name string) (string, error) {
	path := filepath.Join(dir, name)
	_ = os.Remove(path) // 续做时重新备份：VACUUM INTO 要求目标不存在
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
