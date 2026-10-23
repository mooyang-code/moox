package bootstrap

import (
	"context"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// Monitor 的重置（设计 5.2）。
//
//	data：清运行数据——探测结果、网关观测、主机代理登记、告警状态与事件、指标序列与最新值、入库消息、数据到达记录、组件健康、任务清单、标的首见；
//	      保留探测定义与拓扑、告警规则、指标服务登记（配置）；
//	all ：再清探测定义与拓扑、告警规则、指标服务登记（它们由服务目录和策略文件在启动与放置同步时重新生成）。
//
// **通知渠道（含 Webhook）两个范围都不清**：Webhook 是凭据，不属于「回到默认配置」的业务配置；清掉会让告警无处可发。
// 渠道的默认值来自 moox.toml 的 [notification]，用 moox-cli config notification --publish 同步。

var (
	monitorDataTables = []string{
		"t_monitor_check_results", "t_monitor_gateway_observations", "t_monitor_host_agents", "t_monitor_alert_states", "t_monitor_alert_events",
		"t_monitor_metric_series", "t_monitor_metric_latest", "t_monitor_metric_ingest_messages", "t_monitor_bar_arrivals",
		"t_monitor_component_health", "t_monitor_task_inventory", "t_monitor_subject_first_seen",
	}
	monitorConfigTables = []string{
		"t_monitor_topology", "t_monitor_checks", "t_monitor_alert_rules", "t_monitor_metric_services",
	}
	// 引导层：不清。
	monitorBootstrapTables = []string{"t_monitor_notification_channels"}
)

func monitorResetSpec(db *store.Store, dbPath string) *lifecycle.ResetSpec {
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
			Keeps: []string{"数据库表结构", "通知渠道（Webhook 是凭据，默认值来自 moox.toml 的 [notification]）", "发布内的配置文件", "生命周期状态文件"},
			Notes: []string{"重置期间告警静默（只记录、不推送）；重置后探测定义、拓扑、规则由服务目录与策略文件在启动与放置同步时重新生成"},
		}
		if unclassified, _ := lifecycle.ClassifyTables(actual, monitorDataTables, monitorConfigTables, monitorBootstrapTables); len(unclassified) > 0 {
			info.Blockers = append(info.Blockers, "有未归类的表，先更新重置清单："+lifecycle.JoinTables(unclassified))
		}
		tables := append([]string(nil), monitorDataTables...)
		if scope == lifecycle.ResetAll {
			tables = append(tables, monitorConfigTables...)
		} else {
			info.Keeps = append(info.Keeps, "探测定义与拓扑、告警规则、指标服务登记（配置）")
		}
		if rows, err := lifecycle.TableRows(ctx, sqlDB, tables); err == nil {
			info.Stats = map[string]any{"rows": rows}
		}
		steps := []lifecycle.ResetStep{
			{Name: "backup", Describe: "备份 Monitor 数据库到 reset-backups（保留最近 3 份）", Run: func(ctx context.Context, opID string) error {
				backupDir, err := lifecycle.ResetBackupDir(dir, opID, 3)
				if err != nil {
					return err
				}
				path, err := lifecycle.SQLiteBackup(ctx, sqlDB, backupDir, filepath.Base(dbPath))
				if err == nil {
					log.Infof("monitor_reset_backup path=%s", path)
				}
				return err
			}},
			{Name: "clear_tables", Describe: "清空表：" + lifecycle.JoinTables(tables), Run: func(ctx context.Context, _ string) error {
				deleted, err := lifecycle.ClearTables(ctx, sqlDB, tables)
				if err == nil {
					log.Infof("monitor_reset_cleared rows=%v", deleted)
				}
				return err
			}},
		}
		return info, steps, nil
	}}
}
