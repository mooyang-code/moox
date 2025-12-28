package cloudnode

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	"github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr"

	"trpc.group/trpc-go/trpc-go/log"
)

// NodeOperationCompletionHandler 节点操作完成处理器
// 当节点创建或删除Job完成时，触发采集任务实例的重新计算
type NodeOperationCompletionHandler struct {
	taskPlannerService collectmgr.TaskPlannerService
}

// NewNodeOperationCompletionHandler 创建节点操作完成处理器
func NewNodeOperationCompletionHandler(taskPlannerService collectmgr.TaskPlannerService) *NodeOperationCompletionHandler {
	return &NodeOperationCompletionHandler{
		taskPlannerService: taskPlannerService,
	}
}

// CanHandle 判断是否处理该TaskType
// 只处理节点创建和删除类型的Job
func (h *NodeOperationCompletionHandler) CanHandle(taskType string) bool {
	return taskType == asynctask.TaskTypeCreateNode ||
		taskType == asynctask.TaskTypeDeleteNode
}

// OnJobCompleted Job完成时的回调
// 检查Job是否有成功的Task，如果有则触发全量重算
func (h *NodeOperationCompletionHandler) OnJobCompleted(ctx context.Context, job *model.AsyncJob, firstTask *model.AsyncJobTask) error {
	log.InfoContextf(ctx, "[NodeOperationHandler] Processing job %s completion (type=%s, success=%d, failed=%d)",
		job.JobID, firstTask.TaskType, job.SuccessTaskCnt, job.FailedTaskCnt)

	// 检查是否有成功的Task
	if job.SuccessTaskCnt == 0 {
		log.WarnContextf(ctx, "[NodeOperationHandler] Job %s has no success tasks, skip recalculation", job.JobID)
		return nil
	}

	// 触发全量重算
	log.InfoContextf(ctx, "[NodeOperationHandler] Triggering recalculation for job %s", job.JobID)
	result, err := h.taskPlannerService.SyncAllEnabledRules(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeOperationHandler] Recalculation failed for job %s: %v", job.JobID, err)
		// 返回nil避免重试，记录错误日志即可
		return nil
	}

	log.InfoContextf(ctx, "[NodeOperationHandler] Recalculation completed for job %s: synced=%d, created=%d, updated=%d, deleted=%d",
		job.JobID, result.SyncedRules, result.TotalCreated, result.TotalUpdated, result.TotalDeleted)

	return nil
}
