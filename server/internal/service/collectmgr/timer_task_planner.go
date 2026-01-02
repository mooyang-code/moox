package collectmgr

import (
	"context"
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// 全局任务规划器实例（供定时器使用）
var globalTaskPlannerInstance TaskPlannerService

// InitTaskPlannerInstance 初始化全局任务规划器实例
func InitTaskPlannerInstance(service TaskPlannerService) {
	globalTaskPlannerInstance = service
	log.Info("[TaskPlanner] Global task planner instance initialized")
}

// HandleTaskPlannerSchedule 任务实例重算定时器入口函数
// 定时同步所有启用规则的任务实例
func HandleTaskPlannerSchedule(ctx context.Context, params string) error {
	ctxClone := trpc.CloneContext(ctx)

	log.InfoContextf(ctxClone, "[TaskPlanner] Starting scheduled recalculation, params: %s", params)
	startTime := time.Now()

	// 调用全局任务规划器实例
	if globalTaskPlannerInstance == nil {
		err := fmt.Errorf("task planner instance not initialized")
		log.ErrorContext(ctxClone, "[TaskPlanner] "+err.Error())
		return err
	}

	// 执行全量重算
	if err := globalTaskPlannerInstance.RecalculateAllTaskInstances(ctxClone); err != nil {
		log.ErrorContextf(ctxClone, "[TaskPlanner] Scheduled recalculation failed: %v", err)
		return err
	}

	elapsed := time.Since(startTime)
	log.InfoContextf(ctxClone, "[TaskPlanner] Scheduled recalculation completed in %v", elapsed)

	return nil
}
