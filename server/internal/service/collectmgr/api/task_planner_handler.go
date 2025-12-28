package api

import (
	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/common"
	"github.com/mooyang-code/moox/server/internal/errors"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr"
	"trpc.group/trpc-go/trpc-go/log"
)

// TaskPlannerHandler 任务规划器处理器
type TaskPlannerHandler struct {
	taskPlannerService collectmgr.TaskPlannerService
}

// NewTaskPlannerHandler 创建任务规划器处理器
func NewTaskPlannerHandler(taskPlannerService collectmgr.TaskPlannerService) *TaskPlannerHandler {
	return &TaskPlannerHandler{
		taskPlannerService: taskPlannerService,
	}
}

// SyncAllEnabledRules 手动触发全量重算
// POST /api/v1/collect/task_planner/sync_all
func (h *TaskPlannerHandler) SyncAllEnabledRules(c *gin.Context) {
	ctx := c.Request.Context()

	log.InfoContext(ctx, "[TaskPlannerHandler] Manual sync all enabled rules triggered")

	result, err := h.taskPlannerService.SyncAllEnabledRules(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "[TaskPlannerHandler] Failed to sync all enabled rules: %v", err)
		common.HandleAppError(c, errors.Internal("全量重算失败", err))
		return
	}

	log.InfoContextf(ctx, "[TaskPlannerHandler] Sync all completed: %+v", result)
	common.SuccessResponse(c, "全量重算成功", result)
}
