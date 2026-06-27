package monitor

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	"trpc.group/trpc-go/trpc-go/log"
)

var (
	monitorSvc Service
)

// InitMonitorInstance 初始化全局监控服务实例
func InitMonitorInstance(dbManager *database.Manager) {
	monitorSvc = NewService(dbManager)
	log.Info("[Monitor] Monitor service instance initialized")
}

// HandleMonitorSchedule 监控数据采集定时任务（由 tRPC Timer 调用）
func HandleMonitorSchedule(ctx context.Context, params string) error {
	if monitorSvc == nil {
		log.Error("[Monitor] Monitor service not initialized")
		return nil
	}

	if err := monitorSvc.CollectAll(ctx); err != nil {
		log.ErrorContextf(ctx, "[Monitor] Schedule collection failed: %v", err)
		return err
	}

	return nil
}
