package bootstrap

import (
	"context"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// 因子管理的重置（设计 5.2）。
//
//	data：清重算作业与引擎心跳（运行数据）；保留因子集合、因子定义、集合成员（配置）；
//	all ：再清因子集合、因子定义、集合成员，回到新部署的空白状态；默认因子库需要重新导入（moox-cli setup factors）。
//
// Storage 里的因子结果数据集是另一份数据（它们的重置是 Storage 的事）；引擎的本地缓存由引擎自己重置。

var (
	mgrDataTables   = []string{"t_factor_recalc_jobs", "t_factor_engine_heartbeat"}
	mgrConfigTables = []string{"t_factor_sets", "t_factor_defs", "t_factor_set_members"}
)

func mgrResetSpec(db *store.Store, dbPath string) *lifecycle.ResetSpec {
	dir := filepath.Dir(dbPath)
	return &lifecycle.ResetSpec{Plan: func(ctx context.Context, scope lifecycle.ResetScope) (lifecycle.ResetPlanInfo, []lifecycle.ResetStep, error) {
		sqlDB, err := db.SQLDB()
		if err != nil {
			return lifecycle.ResetPlanInfo{}, nil, err
		}
		lifecycle.RegisterSQLiteFile(sqlDB, dbPath)
		actual, err := lifecycle.BusinessTables(ctx, sqlDB)
		if err != nil {
			return lifecycle.ResetPlanInfo{}, nil, err
		}
		info := lifecycle.ResetPlanInfo{
			Keeps: []string{"数据库表结构", "Storage 里的因子结果数据集", "发布内的配置文件", "生命周期状态文件"},
			Notes: []string{"重置后默认因子库要重新导入：moox-cli setup factors"},
		}
		if unclassified, _ := lifecycle.ClassifyTables(actual, mgrDataTables, mgrConfigTables); len(unclassified) > 0 {
			info.Blockers = append(info.Blockers, "有未归类的表，先更新重置清单："+lifecycle.JoinTables(unclassified))
		}
		tables := append([]string(nil), mgrDataTables...)
		if scope == lifecycle.ResetAll {
			tables = append(tables, mgrConfigTables...)
		} else {
			info.Keeps = append(info.Keeps, "因子集合、因子定义、集合成员（配置）")
		}
		if rows, err := lifecycle.TableRows(ctx, sqlDB, tables); err == nil {
			info.Stats = map[string]any{"rows": rows}
		}
		steps := []lifecycle.ResetStep{
			{Name: "backup", Describe: "备份因子管理数据库到 reset-backups（保留最近 3 份）", Run: func(ctx context.Context, opID string) error {
				backupDir, err := lifecycle.ResetBackupDir(dir, opID, 3)
				if err != nil {
					return err
				}
				path, err := lifecycle.SQLiteBackup(ctx, sqlDB, backupDir, filepath.Base(dbPath))
				if err == nil {
					log.Infof("factor_mgr_reset_backup path=%s", path)
				}
				return err
			}},
			{Name: "clear_tables", Describe: "清空表：" + lifecycle.JoinTables(tables), Run: func(ctx context.Context, _ string) error {
				deleted, err := lifecycle.ClearTables(ctx, sqlDB, tables)
				if err == nil {
					log.Infof("factor_mgr_reset_cleared rows=%v", deleted)
				}
				return err
			}},
		}
		return info, steps, nil
	}}
}
