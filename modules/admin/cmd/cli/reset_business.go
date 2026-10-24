package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// moox-admin-cli reset-business：系统重置里 Admin 的业务部分（设计 5.2）。
//
// 清掉业务空间及成员（默认空间除外）、采集发布租约与操作、活动日志；
// 保留引导身份层——用户、登录历史、密钥、主机与放置、SSH、主机网关状态——它们决定谁能进来、能连哪些机器，
// 清掉后重置自己就无法继续。默认空间由 setup init 重建，所以这里也保留它。
// 重置前用 VACUUM INTO 备份整个库到数据库所在目录的 reset-backups/（保留最近 3 份），这个命令不依赖 Admin 进程是否在运行。

// defaultSpaceID 是随系统创建的默认空间，业务重置保留它。
const defaultSpaceID = "mooxsys"

// businessResetData 是运行数据：重置的两个范围都清。
var businessResetData = []string{"t_activity_logs", "t_collector_publish_leases", "t_collector_publish_operations"}

// businessResetConfig 是业务配置：只有 --scope all 清（默认空间除外）。
var businessResetConfig = []string{"t_space_members", "t_spaces"}

// businessKeep 是引导身份层与登录记录：任何范围都不清。
var businessKeep = []string{
	"t_host_gateway_status", "t_hosts", "t_login_history", "t_deployments", "t_secrets",
	"t_ssh_host", "t_ssh_host_key", "t_ssh_session", "t_users",
}

type businessResetResult struct {
	Module  string           `json:"module"`
	Action  string           `json:"action"`
	Status  string           `json:"status"`
	Scope   string           `json:"scope"`
	DBPath  string           `json:"db_path"`
	Backup  string           `json:"backup,omitempty"`
	Deleted map[string]int64 `json:"deleted"`
	Keeps   []string         `json:"keeps"`
}

func isResetBusinessCommand(args []string) bool {
	return len(args) > 1 && args[1] == "reset-business"
}

func runResetBusinessCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fs := flag.NewFlagSet("reset-business", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath, scope := defaultInitDBPath, "data"
	var dryRun, yes bool
	fs.StringVar(&dbPath, "db-path", dbPath, "Admin 的 SQLite 数据库路径")
	fs.StringVar(&scope, "scope", scope, "重置范围：data（活动日志、采集发布租约）| all（再加业务空间及成员，默认空间保留）")
	fs.BoolVar(&dryRun, "dry-run", false, "只列出会删除的行数，不改动")
	fs.BoolVar(&yes, "yes", false, "确认执行（会删除数据；执行前先备份）")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("多余的参数：%s", strings.Join(fs.Args(), " "))
	}
	if scope != "data" && scope != "all" {
		return fmt.Errorf("范围只能是 data 或 all，收到 %q", scope)
	}
	if !dryRun && !yes {
		return fmt.Errorf("重置会删除数据，需要加 --yes 确认（先用 --dry-run 看会删什么）")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("找不到数据库 %s：%w", dbPath, err)
	}
	db, err := gorm.Open(sqlite.Open(initSQLiteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("打开数据库：%w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	result, err := resetBusiness(context.Background(), sqlDB, dbPath, scope, dryRun)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

// resetBusiness 备份后清理；dryRun 只统计。表没有归类（schema 新增了表）时拒绝执行。
func resetBusiness(ctx context.Context, db *sql.DB, dbPath, scope string, dryRun bool) (businessResetResult, error) {
	result := businessResetResult{Module: "admin", Action: "reset-business", Scope: scope, DBPath: dbPath, Deleted: map[string]int64{}, Keeps: businessKeep}
	if err := checkBusinessTables(ctx, db); err != nil {
		return result, err
	}
	tables := append([]string(nil), businessResetData...)
	if scope == "all" {
		tables = append(tables, businessResetConfig...)
	}
	if dryRun {
		result.Status = "dry-run"
		for _, t := range tables {
			n, err := countBusinessRows(ctx, db, t)
			if err != nil {
				return result, err
			}
			result.Deleted[t] = n
		}
		return result, nil
	}
	dir := filepath.Join(filepath.Dir(dbPath), "reset-backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return result, err
	}
	backup := filepath.Join(dir, "admin-"+time.Now().UTC().Format("20060102T150405.000")+".db")
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
		return result, fmt.Errorf("备份数据库到 %s：%w", backup, err)
	}
	pruneBackups(dir, 3)
	result.Backup = backup
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return result, err
	}
	for _, t := range tables {
		query := "DELETE FROM " + t
		var args []any
		if t == "t_spaces" || t == "t_space_members" {
			query += " WHERE c_space_id <> ?"
			args = append(args, defaultSpaceID)
		}
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return result, fmt.Errorf("清理 %s：%w", t, err)
		}
		result.Deleted[t], _ = res.RowsAffected()
		if len(args) == 0 {
			_, _ = tx.ExecContext(ctx, "DELETE FROM sqlite_sequence WHERE name = ?", t)
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.Status = "ok"
	return result, nil
}

func checkBusinessTables(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 't\\_%' ESCAPE '\\'")
	if err != nil {
		return err
	}
	defer rows.Close()
	known := map[string]bool{}
	for _, group := range [][]string{businessResetData, businessResetConfig, businessKeep} {
		for _, t := range group {
			known[t] = true
		}
	}
	var unclassified []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if !known[name] {
			unclassified = append(unclassified, name)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		return fmt.Errorf("有未归类的表，先更新重置清单：%s", strings.Join(unclassified, "、"))
	}
	return nil
}

func countBusinessRows(ctx context.Context, db *sql.DB, table string) (int64, error) {
	query := "SELECT COUNT(*) FROM " + table
	var args []any
	if table == "t_spaces" || table == "t_space_members" {
		query += " WHERE c_space_id <> ?"
		args = append(args, defaultSpaceID)
	}
	var n int64
	err := db.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

// pruneBackups 只保留最近 keep 份备份（文件名以时间结尾，字典序即时间序）。
func pruneBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "admin-") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}
