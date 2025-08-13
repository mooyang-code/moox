package logic

import (
	"context"
	"encoding/json"
	"fmt"

	asynctasklogic "github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	asynctaskmodel "github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	cloudnodelogic "github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// BatchDeleteNodeRequest 批量删除节点请求
type BatchDeleteNodeRequest struct {
	Nodes []string `json:"nodes"` // 节点ID列表
}

// BatchDeleteNodeExecutor 批量删除节点执行器
type BatchDeleteNodeExecutor struct {
	scfNodeService   cloudnodelogic.SCFNodeService
	asyncTaskService asynctasklogic.AsyncTaskService
	db               *gorm.DB
}

// NewBatchDeleteNodeExecutor 创建批量删除节点执行器
func NewBatchDeleteNodeExecutor(db *gorm.DB, scfNodeService cloudnodelogic.SCFNodeService, asyncTaskService asynctasklogic.AsyncTaskService) *BatchDeleteNodeExecutor {
	return &BatchDeleteNodeExecutor{
		scfNodeService:   scfNodeService,
		asyncTaskService: asyncTaskService,
		db:               db,
	}
}

// GetTaskType 返回任务类型
func (e *BatchDeleteNodeExecutor) GetTaskType() string {
	return asynctaskmodel.TaskTypeBatchDeleteNode
}

// ValidateRequest 验证任务请求
func (e *BatchDeleteNodeExecutor) ValidateRequest(taskData string) error {
	var request BatchDeleteNodeRequest
	if err := json.Unmarshal([]byte(taskData), &request); err != nil {
		return fmt.Errorf("invalid request format: %w", err)
	}

	if len(request.Nodes) == 0 {
		return fmt.Errorf("no nodes to delete")
	}

	return nil
}

// Execute 执行批量删除任务
func (e *BatchDeleteNodeExecutor) Execute(ctx context.Context, task *asynctaskmodel.AsyncTask) error {
	log.InfoContextf(ctx, "Starting batch delete node task: %s", task.TaskID)

	// 解析请求参数
	var request BatchDeleteNodeRequest
	if err := json.Unmarshal([]byte(task.RequestParams), &request); err != nil {
		errorMsg := fmt.Sprintf("failed to parse request params: %v", err)
		e.asyncTaskService.CompleteTask(ctx, task.TaskID, asynctaskmodel.TaskStatusFailed, nil, errorMsg)
		return fmt.Errorf(errorMsg)
	}

	// 创建任务详情
	var taskItems []asynctasklogic.TaskItem
	for _, nodeID := range request.Nodes {
		taskItems = append(taskItems, asynctasklogic.TaskItem{
			ItemID:   nodeID,
			ItemName: fmt.Sprintf("Node %s", nodeID),
		})
	}

	if err := e.asyncTaskService.BatchCreateTaskDetails(ctx, task.TaskID, taskItems); err != nil {
		log.ErrorContextf(ctx, "Failed to create task details: %v", err)
	}

	// 将节点删除任务加入队列
	enqueuedCount := 0
	failedToEnqueueCount := 0

	for _, nodeID := range request.Nodes {
		// 更新任务详情状态为处理中
		e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, nodeID,
			asynctaskmodel.TaskDetailStatusProcessing, "")

		// 将节点删除任务加入队列（实际删除将由Worker异步执行）
		err := e.scfNodeService.RemoveNode(ctx, nodeID, task.TaskID, nodeID)
		if err != nil {
			failedToEnqueueCount++
			errorMsg := fmt.Sprintf("将节点加入删除队列失败: %v", err)
			log.ErrorContextf(ctx, "Failed to enqueue node deletion for %s: %v", nodeID, err)

			// 如果无法加入队列，直接标记为失败
			e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, nodeID,
				asynctaskmodel.TaskDetailStatusFailed, errorMsg)
		} else {
			enqueuedCount++
			log.InfoContextf(ctx, "Successfully enqueued node %s for deletion; taskID:%s", nodeID, task.TaskID)
			// 注意：这里不再立即更新为成功状态，保持处理中状态
			// 实际的成功/失败状态将由NodeDeletionWorker在删除完成后更新
		}
	}

	// 记录任务创建情况
	if failedToEnqueueCount == 0 {
		log.InfoContextf(ctx, "All nodes enqueued for deletion. Total: %d", len(request.Nodes))
	} else if enqueuedCount == 0 {
		log.ErrorContextf(ctx, "Failed to enqueue any nodes for deletion. Total: %d", len(request.Nodes))
		// 如果所有节点都无法加入队列，直接标记任务失败
		resultData := map[string]interface{}{
			"total_count":   len(request.Nodes),
			"success_count": 0,
			"failed_count":  failedToEnqueueCount,
		}
		return e.asyncTaskService.CompleteTask(ctx, task.TaskID, asynctaskmodel.TaskStatusFailed, resultData, "所有节点都无法加入删除队列")
	} else {
		log.WarnContextf(ctx, "Partially enqueued nodes for deletion. Total: %d, Enqueued: %d, Failed: %d",
			len(request.Nodes), enqueuedCount, failedToEnqueueCount)
	}

	// 任务已经提交到队列，等待Worker处理
	// 注意：这里不再调用CompleteTask，任务将保持处理中状态，直到所有Worker完成处理
	return nil
}
