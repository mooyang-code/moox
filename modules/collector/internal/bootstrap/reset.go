package bootstrap

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// Collector 的重置（设计 5.2）。
//
//	data：清运行数据——补采作业与分块、自动补采水位、历史扫描、行情源缺失、合成快照与历史作业、补写通知与事件发件箱、流采集工作进程心跳、标签标的同步的运行结果；
//	      保留采集任务、采集计划、采集方式、标的排除（配置）；
//	all ：再清采集任务（含标签、序列）、采集计划、采集方式、标的排除，回到新部署的状态（采集任务由启动时的种子重新写入）。
//
// 采集暂停开关表（t_collector_fetch_plan_pauses）两个范围都不清：它承载生命周期的暂停，重置期间必须保持暂停（实时采集关闭），
// 到用户重置后第一次 init 才放开（AfterReset）。重置**不动云函数本身**：SCF 部署包是外部资源；
// 但执行前要求所有空间的 SCF 定时器已确认停用（暂停时已停用），否则采集计划被删后定时器会成为没人管理的孤儿。

var (
	collectorDataTables = []string{
		"t_collector_kline_backfill_jobs", "t_collector_kline_backfill_chunks", "t_collector_backfill_watermarks",
		"t_collector_history_scans", "t_collector_source_absences", "t_collector_resample_snapshots", "t_collector_resample_jobs", "t_collector_backfill_notices", "t_collector_event_outbox",
		"t_collector_stream_workers", "t_collector_tag_sync_runs",
	}
	collectorConfigTables = []string{
		"t_collector_tasks", "t_collector_task_tags", "t_collector_task_series", "t_collector_fetch_plans",
		"t_collector_ingest_modes", "t_collector_subject_exclusions",
	}
	// 生命周期状态：不清，由 AfterReset 在用户 init 后放开。
	collectorStateTables = []string{"t_collector_fetch_plan_pauses"}
)

func collectorResetSpec(db *store.Store, dbPath string, control *collectorControl) *lifecycle.ResetSpec {
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
			Keeps: []string{"数据库表结构", "采集暂停开关（重置期间保持暂停，重置后第一次 init 才放开）", "云函数本身（SCF 部署包是外部资源）", "发布内的配置文件", "生命周期状态文件"},
			Notes: []string{"重置后采集计划要由采集任务重新生成；Storage 数据被清空时还要对每个有采集计划的空间执行 collector fetch-plan reregister"},
		}
		if unclassified, _ := lifecycle.ClassifyTables(actual, collectorDataTables, collectorConfigTables, collectorStateTables); len(unclassified) > 0 {
			info.Blockers = append(info.Blockers, "有未归类的表，先更新重置清单："+lifecycle.JoinTables(unclassified))
		}
		tables := append([]string(nil), collectorDataTables...)
		if scope == lifecycle.ResetAll {
			tables = append(tables, collectorConfigTables...)
		} else {
			info.Keeps = append(info.Keeps, "采集任务、采集计划、采集方式、标的排除（配置）")
		}
		if rows, err := lifecycle.TableRows(ctx, sqlDB, tables); err == nil {
			info.Stats = map[string]any{"rows": rows}
		}
		steps := []lifecycle.ResetStep{
			// 定时器停用是暂停动作的一部分，只有暂停之后才成立，所以不能放进 plan 的阻止项（plan 时模块还在运行，永远发不出令牌）；
			// 放在第一步：删除采集计划之前必须确认，否则定时器会成为没人管理的孤儿。
			{Name: "verify_scf_timers_disabled", Describe: "核验所有空间的 SCF 定时器已停用", Run: func(ctx context.Context, _ string) error {
				return control.waitTimersDisabled(ctx)
			}},
			{Name: "quiesce_backfill", Describe: "等待在途的补采分块结束", Run: func(ctx context.Context, _ string) error {
				waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				return control.quiesceBackfill(waitCtx)
			}},
			{Name: "backup", Describe: "备份 Collector 数据库到 reset-backups（保留最近 3 份）", Run: func(ctx context.Context, opID string) error {
				backupDir, err := lifecycle.ResetBackupDir(dir, opID, 3)
				if err != nil {
					return err
				}
				path, err := lifecycle.SQLiteBackup(ctx, sqlDB, backupDir, filepath.Base(dbPath))
				if err == nil {
					log.Infof("collector_reset_backup path=%s", path)
				}
				return err
			}},
			{Name: "clear_tables", Describe: "清空表：" + lifecycle.JoinTables(tables), Run: func(ctx context.Context, _ string) error {
				deleted, err := lifecycle.ClearTables(ctx, sqlDB, tables)
				if err == nil {
					log.Infof("collector_reset_cleared rows=%v", deleted)
				}
				return err
			}},
		}
		return info, steps, nil
	}}
}

// timersNotDisabled 返回 SCF 定时器还没有确认停用的空间；没有绑定采集计划管理器（没有配置采集计划）时为空。
func (c *collectorControl) timersNotDisabled() []string {
	c.mu.Lock()
	manager, spaces := c.manager, append([]string(nil), c.spaces...)
	c.mu.Unlock()
	if manager == nil {
		return nil
	}
	var waiting []string
	for _, space := range spaces {
		if !manager.TimersDisabled(space) {
			waiting = append(waiting, space)
		}
	}
	return waiting
}

// waitTimersDisabled 等所有空间的 SCF 定时器确认停用（暂停刚结束时可能还差几秒），超时报错。
func (c *collectorControl) waitTimersDisabled(ctx context.Context) error {
	wait := c.timerWait
	if wait <= 0 {
		wait = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		waiting := c.timersNotDisabled()
		if len(waiting) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("空间 %s 的 SCF 定时器还没有确认停用：先暂停并等定时器停用，否则删除采集计划后定时器会成为孤儿", strings.Join(waiting, "、"))
		case <-ticker.C:
		}
	}
}

func (c *collectorControl) quiesceBackfill(ctx context.Context) error {
	c.mu.Lock()
	backfiller := c.backfiller
	c.mu.Unlock()
	if backfiller == nil {
		return nil
	}
	if err := backfiller.Quiesce(ctx); err != nil {
		return fmt.Errorf("等待补采分块结束：%w", err)
	}
	return nil
}

// afterReset 在重置后用户第一次 init 时放开采集暂停开关，实时采集随对账协程恢复。
func (c *collectorControl) afterReset(ctx context.Context) {
	c.setRecovering(false)
	if err := c.setPaused(ctx, false); err != nil {
		log.ErrorContextf(ctx, "collector_reset_unpause_failed error=%v", err)
		return
	}
	log.InfoContextf(ctx, "collector_reset_unpaused")
}
