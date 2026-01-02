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

// RecalculateAllTaskInstances 手动触发全量重算
// POST /api/v1/collect/task_planner/recalculate_all
func (h *TaskPlannerHandler) RecalculateAllTaskInstances(c *gin.Context) {
	ctx := c.Request.Context()

	log.InfoContext(ctx, "[TaskPlannerHandler] Manual recalculation triggered for all task instances")

	if err := h.taskPlannerService.RecalculateAllTaskInstances(ctx); err != nil {
		log.ErrorContextf(ctx, "[TaskPlannerHandler] Failed to recalculate all task instances: %v", err)
		common.HandleAppError(c, errors.Internal("全量重算失败", err))
		return
	}

	log.InfoContext(ctx, "[TaskPlannerHandler] Recalculation completed successfully")
	common.SuccessResponse(c, "全量重算成功", nil)
}
