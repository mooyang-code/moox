package monitor

import (
	"context"

	"trpc.group/trpc-go/trpc-go/log"
)

// HandleMonitorCleanupSchedule 监控历史数据清理定时任务（由 tRPC Timer 调用）
// 每天0点执行，删除 7 天前的历史数据
func HandleMonitorCleanupSchedule(ctx context.Context, params string) error {
	if monitorSvc == nil {
		log.Error("[Monitor] Monitor service not initialized, skip cleanup")
		return nil
	}

	log.InfoContext(ctx, "[Monitor] Start cleaning old history data...")
	if err := monitorSvc.CleanHistory(ctx, 7); err != nil {
		log.ErrorContextf(ctx, "[Monitor] Cleanup schedule failed: %v", err)
		return err
	}

	return nil
}
