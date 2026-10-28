package bootstrap

import (
	"context"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// 策略模块的重置（设计 5.2）。
//
//	data：清运行数据——结果与明细、回放、年龄通过记录；保留策略定义、实例与会话（配置）；
//	all ：再清策略定义与版本、实例、会话与模板，回到新部署的空白状态（策略没有种子）。
//
// 重置不会替 Trade 解除实例对逻辑账户的认领：那是系统级重置的事（先重置策略再重置 Trade，Trade 的逻辑账户一并清掉）。

// 策略数据库的业务表分类；Plan 校验库里的每张表都已归类，schema 新增表而忘了归类时重置会拒绝执行。
var (
	strategyDataTables = []string{
		"t_strategy_results", "t_strategy_result_items", "t_strategy_replays", "t_strategy_replay_bars", "t_strategy_age_passed",
	}
	strategyConfigTables = []string{
		"t_strategy_defs", "t_strategy_def_versions", "t_strategy_instances", "t_strategy_sessions", "t_strategy_display_settings", "t_strategy_templates",
	}
)

func strategyResetSpec(db *store.Store, dbPath string) *lifecycle.ResetSpec {
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
			Keeps: []string{"数据库表结构", "发布内的配置文件", "生命周期状态文件"},
			Notes: []string{"不会解除实例在 Trade 里对逻辑账户的认领：系统级重置先重置策略再重置 Trade"},
		}
		if unclassified, _ := lifecycle.ClassifyTables(actual, strategyDataTables, strategyConfigTables); len(unclassified) > 0 {
			info.Blockers = append(info.Blockers, "有未归类的表，先更新重置清单："+lifecycle.JoinTables(unclassified))
		}
		tables := append([]string(nil), strategyDataTables...)
		if scope == lifecycle.ResetAll {
			tables = append(tables, strategyConfigTables...)
		} else {
			info.Keeps = append(info.Keeps, "策略定义、实例、会话（配置）")
		}
		if rows, err := lifecycle.TableRows(ctx, sqlDB, tables); err == nil {
			info.Stats = map[string]any{"rows": rows}
		}
		steps := []lifecycle.ResetStep{
			{Name: "backup", Describe: "备份策略数据库到 reset-backups（保留最近 3 份）", Run: func(ctx context.Context, opID string) error {
				backupDir, err := lifecycle.ResetBackupDir(dir, opID, 3)
				if err != nil {
					return err
				}
				path, err := lifecycle.SQLiteBackup(ctx, sqlDB, backupDir, filepath.Base(dbPath))
				if err == nil {
					log.Infof("strategy_reset_backup path=%s", path)
				}
				return err
			}},
			{Name: "clear_tables", Describe: "清空表：" + lifecycle.JoinTables(tables), Run: func(ctx context.Context, _ string) error {
				deleted, err := lifecycle.ClearTables(ctx, sqlDB, tables)
				if err == nil {
					log.Infof("strategy_reset_cleared rows=%v", deleted)
				}
				return err
			}},
		}
		return info, steps, nil
	}}
}
