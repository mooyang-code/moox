package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// 因子引擎的重置（设计 5.1、5.2）：**进程内**执行，引擎进程不停——健康端口（生命周期接口）由引擎进程提供，
// 重置期间业务任务已停（模块暂停，预载、追赶、补算都受生命周期门禁约束）；Pebble 不关闭，用范围删除清空再压缩回收空间。
//
//	data：清本地时序存储（Pebble 里的 K 线、边界、同步位点、失效重算待办）、连续水位、空洞账本，并丢弃内存里的窗口；
//	all ：再清目录缓存（FactorMgr 配置的副本，由下一次同步重新取得）。
//
// 本地时序存储**不备份**：它是 Storage 数据的本地副本，可以从 Storage 回补（体积大、可重新积累）；
// 水位、账本、目录缓存这些小文件备份到 reset-backups。需要离线停引擎的 reconcile / backfill 是另一回事，不在这里。

func (r *Runtime) resetSpec() *lifecycle.ResetSpec {
	return &lifecycle.ResetSpec{Plan: func(ctx context.Context, scope lifecycle.ResetScope) (lifecycle.ResetPlanInfo, []lifecycle.ResetStep, error) {
		info := lifecycle.ResetPlanInfo{
			Keeps: []string{"引擎配置文件 engine.yaml", "生命周期状态文件", "Storage 里的行情与因子结果（它们的重置是 Storage 的事）"},
			Notes: []string{"本地时序存储不备份（Storage 数据的本地副本，可回补）；水位、空洞账本、目录缓存备份到 reset-backups"},
		}
		if scope == lifecycle.ResetData {
			info.Keeps = append(info.Keeps, "目录缓存（FactorMgr 配置的副本）")
		}
		stats := map[string]any{}
		if r.series != nil {
			stats["local_store_bytes"] = r.series.DiskUsage()
		}
		if r.watermarks != nil {
			stats["watermarks"] = len(r.watermarks.All())
		}
		if r.gap != nil {
			stats["gap_pending"] = r.gap.PendingCount()
		}
		info.Stats = stats

		steps := []lifecycle.ResetStep{
			{Name: "backup_state", Describe: "备份连续水位、空洞账本、目录缓存到 reset-backups（保留最近 3 份）", Run: r.backupEngineState},
			{Name: "drop_memory", Describe: "丢弃内存里的数据源与窗口，忘掉预载记录", Run: func(context.Context, string) error {
				r.data.Reset()
				r.preload.Forget()
				return nil
			}},
			{Name: "wipe_local_store", Describe: "清空本地时序存储（范围删除并压缩回收空间）", Run: func(context.Context, string) error {
				if err := r.series.Wipe(); err != nil {
					return err
				}
				log.Infof("factor_reset_local_store_wiped")
				return nil
			}},
			{Name: "clear_watermarks", Describe: "清空连续水位", Run: func(context.Context, string) error { return r.watermarks.Reset() }},
			{Name: "clear_gap_ledger", Describe: "清空空洞账本", Run: func(context.Context, string) error {
				removed, err := r.gap.Clear()
				log.Infof("factor_reset_gap_ledger_cleared files=%d", removed)
				return err
			}},
		}
		if scope == lifecycle.ResetAll {
			steps = append(steps, lifecycle.ResetStep{Name: "clear_catalog", Describe: "清空目录缓存（下一次同步重新取得）", Run: func(context.Context, string) error { return r.catalog.Clear() }})
		}
		return info, steps, nil
	}}
}

// backupEngineState 把水位、账本、目录缓存这几个小文件拷到备份目录。
func (r *Runtime) backupEngineState(_ context.Context, operationID string) error {
	stateDir := filepath.Dir(r.cfg.Lifecycle.StateFile)
	backupDir, err := lifecycle.ResetBackupDir(stateDir, operationID, 3)
	if err != nil {
		return err
	}
	var files []string
	if r.watermarks != nil {
		files = append(files, r.watermarks.Path())
	}
	if r.cfg.CatalogSync.StateFile != "" {
		files = append(files, r.cfg.CatalogSync.StateFile)
	}
	if r.cfg.Gap.LedgerDir != "" {
		ledgers, _ := filepath.Glob(filepath.Join(r.cfg.Gap.LedgerDir, "factor-gap-*.jsonl"))
		files = append(files, ledgers...)
	}
	for _, file := range files {
		if err := copyFile(file, filepath.Join(backupDir, filepath.Base(file))); err != nil {
			return fmt.Errorf("备份 %s：%w", file, err)
		}
	}
	return nil
}

// copyFile 拷贝文件；源文件不存在视为没有（例如还没有账本）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
