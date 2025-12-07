package collectmgr

import (
	"context"
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	collectordao "github.com/mooyang-code/moox/server/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/distributor"
	"github.com/mooyang-code/moox/server/internal/service/database"

	"trpc.group/trpc-go/trpc-go/log"
)

// 全局变量
var (
	globalTaskPlannerInstance *TaskPlannerServiceImpl // 全局任务规划器实例
	taskPlannerInstanceOnce   sync.Once               // 确保单例初始化
)

// InitTaskPlannerInstance 初始化全局任务规划器实例（供 bootstrap 调用）
func InitTaskPlannerInstance(dbManager *database.Manager) {
	taskPlannerInstanceOnce.Do(func() {
		log.Info("[TaskPlanner] Initializing global task planner instance...")

		// 创建 DAO
		taskRulesDAO := collectordao.NewCollectorTaskRulesDAO(dbManager.GetDB())
		instanceDAO := collectordao.NewCollectorTaskInstanceDAO(dbManager.GetDB())
		nodeDAO := dao.NewCloudNodeDAO(dbManager.GetDB())

		// 创建分配器注册表
		registry := distributor.NewDistributorRegistry(nodeDAO, nil) // TODO: 传入 SymbolProvider 实现

		// 创建任务规划器实例
		globalTaskPlannerInstance = NewTaskPlannerServiceImpl(
			taskRulesDAO,
			instanceDAO,
			registry,
		).(*TaskPlannerServiceImpl)
		log.Info("[TaskPlanner] Global task planner instance initialized")
	})
}

// GetTaskPlannerInstance 获取全局任务规划器实例
func GetTaskPlannerInstance() TaskPlannerService {
	return globalTaskPlannerInstance
}

// TaskSyncSchedule TRPC定时器[入口函数] - 定时同步所有启用的规则
func TaskSyncSchedule(ctx context.Context, params string) error {
	log.InfoContextf(ctx, "[TaskPlanner] Starting task sync schedule, params: %s", params)

	if globalTaskPlannerInstance == nil {
		err := fmt.Errorf("task planner instance not initialized")
		log.ErrorContextf(ctx, "[TaskPlanner] %v", err)
		return err
	}

	// 执行同步所有启用的规则
	result, err := globalTaskPlannerInstance.SyncAllEnabledRules(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "[TaskPlanner] Task sync failed: %v", err)
		return err
	}

	log.InfoContextf(ctx, "[TaskPlanner] Task sync schedule completed: "+
		"total=%d, synced=%d, failed=%d, created=%d, updated=%d, deleted=%d",
		result.TotalRules, result.SyncedRules, result.FailedRules,
		result.TotalCreated, result.TotalUpdated, result.TotalDeleted)
	return nil
}
