package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// Archive 管理端的重置（设计 5.2）。
//
//	data：清归档运行、分区状态、worker 登记；保留归档任务（配置）；
//	all ：再清归档任务，回到新部署的空白状态。
//
// **已归档的 Parquet 文件不删**（它们在 worker 的归档目录里，不属于管理库），所以任务被重建后可能重复归档旧区间：
// 需要彻底重来时，另外清理 worker 主机上的归档目录与暂存库 `.state/staging.db`（暂存数据、水位和永久分区状态——不清的话
// 重建同名任务会沿用旧水位甚至重新载入旧 Parquet）。归档 worker 是外部程序，它的暂存库重置不在这里，执行前应先停掉 worker。
// 还有运行在排队或执行中时拒绝重置。

var (
	archiveDataTables   = []string{"t_archive_runs", "t_archive_partitions", "t_archive_workers"}
	archiveConfigTables = []string{"t_archive_tasks"}
)

func activeRuns(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM t_archive_runs WHERE c_status IN ('queued', 'running')").Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("检查未结束的归档运行：%w", err)
	}
	return n, nil
}

func archiveResetSpec(db *store.Store, dbPath string) *lifecycle.ResetSpec {
	dir := filepath.Dir(dbPath)
	return &lifecycle.ResetSpec{Plan: func(ctx context.Context, scope lifecycle.ResetScope) (lifecycle.ResetPlanInfo, []lifecycle.ResetStep, error) {
		sqlDB, err := db.SQLDB()
		if err != nil {
			return lifecycle.ResetPlanInfo{}, nil, err
		}
		actual, err := lifecycle.BusinessTables(ctx, sqlDB)
		if err != nil {
			return lifecycle.ResetPlanInfo{}, nil, err
		}
		info := lifecycle.ResetPlanInfo{
			Keeps: []string{"数据库表结构", "已归档的 Parquet 文件（在 worker 的归档目录里）", "归档 worker 的暂存库 .state/staging.db（外部程序，需另行重置）", "发布内的配置文件", "生命周期状态文件"},
			Notes: []string{"重建同名任务可能重复归档旧区间；需要彻底重来时先停 archive-worker 并清理它的归档目录与暂存库"},
		}
		if unclassified, _ := lifecycle.ClassifyTables(actual, archiveDataTables, archiveConfigTables); len(unclassified) > 0 {
			info.Blockers = append(info.Blockers, "有未归类的表，先更新重置清单："+lifecycle.JoinTables(unclassified))
		}
		if n, err := activeRuns(ctx, sqlDB); err != nil {
			return lifecycle.ResetPlanInfo{}, nil, err
		} else if n > 0 {
			info.Blockers = append(info.Blockers, fmt.Sprintf("还有 %d 个排队或执行中的归档运行：等它们结束或先取消", n))
		}
		tables := append([]string(nil), archiveDataTables...)
		if scope == lifecycle.ResetAll {
			tables = append(tables, archiveConfigTables...)
		} else {
			info.Keeps = append(info.Keeps, "归档任务（配置）")
		}
		if rows, err := lifecycle.TableRows(ctx, sqlDB, tables); err == nil {
			info.Stats = map[string]any{"rows": rows}
		}
		steps := []lifecycle.ResetStep{
			{Name: "verify_no_active_runs", Describe: "核验没有排队或执行中的归档运行", Run: func(ctx context.Context, _ string) error {
				n, err := activeRuns(ctx, sqlDB)
				if err != nil {
					return err
				}
				if n > 0 {
					return fmt.Errorf("拒绝重置：还有 %d 个排队或执行中的归档运行", n)
				}
				return nil
			}},
			{Name: "backup", Describe: "备份归档管理数据库到 reset-backups（保留最近 3 份）", Run: func(ctx context.Context, opID string) error {
				backupDir, err := lifecycle.ResetBackupDir(dir, opID, 3)
				if err != nil {
					return err
				}
				path, err := lifecycle.SQLiteBackup(ctx, sqlDB, backupDir, filepath.Base(dbPath))
				if err == nil {
					log.Infof("archive_reset_backup path=%s", path)
				}
				return err
			}},
			{Name: "clear_tables", Describe: "清空表：" + lifecycle.JoinTables(tables), Run: func(ctx context.Context, _ string) error {
				deleted, err := lifecycle.ClearTables(ctx, sqlDB, tables)
				if err == nil {
					log.Infof("archive_reset_cleared rows=%v", deleted)
				}
				return err
			}},
		}
		return info, steps, nil
	}}
}
