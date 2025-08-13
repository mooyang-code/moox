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

// NodeDeployInfo 节点部署信息
type NodeDeployInfo struct {
	NodeID         string `json:"node_id"`
	ZipFileBase64  string `json:"zip_file_base64"`
	FileName       string `json:"file_name"`
}

// BatchDeployNodeRequest 批量部署节点请求
type BatchDeployNodeRequest struct {
	Nodes []NodeDeployInfo `json:"nodes"` // 节点部署信息列表
}

// BatchDeployNodeExecutor 批量部署节点执行器
type BatchDeployNodeExecutor struct {
	scfNodeService   cloudnodelogic.SCFNodeService
	asyncTaskService asynctasklogic.AsyncTaskService
	db               *gorm.DB
}

// NewBatchDeployNodeExecutor 创建批量部署节点执行器
func NewBatchDeployNodeExecutor(db *gorm.DB, scfNodeService cloudnodelogic.SCFNodeService, asyncTaskService asynctasklogic.AsyncTaskService) *BatchDeployNodeExecutor {
	return &BatchDeployNodeExecutor{
		scfNodeService:   scfNodeService,
		asyncTaskService: asyncTaskService,
		db:               db,
	}
}

// GetTaskType 返回任务类型
func (e *BatchDeployNodeExecutor) GetTaskType() string {
	return asynctaskmodel.TaskTypeBatchDeployNode
}

// ValidateRequest 验证任务请求
func (e *BatchDeployNodeExecutor) ValidateRequest(taskData string) error {
	log.Infof("BatchDeployNodeExecutor.ValidateRequest - taskData: %s", taskData)
	
	var request BatchDeployNodeRequest
	if err := json.Unmarshal([]byte(taskData), &request); err != nil {
		log.Errorf("Failed to unmarshal request: %v, taskData: %s", err, taskData)
		return fmt.Errorf("invalid request format: %w", err)
	}

	log.Infof("Parsed request - nodes count: %d", len(request.Nodes))
	
	if len(request.Nodes) == 0 {
		return fmt.Errorf("no nodes to deploy")
	}

	// 验证每个节点的部署信息
	for i, node := range request.Nodes {
		if node.NodeID == "" {
			return fmt.Errorf("node[%d]: node_id is required", i)
		}
		if node.ZipFileBase64 == "" {
			return fmt.Errorf("node[%d]: zip_file_base64 is required", i)
		}
		if node.FileName == "" {
			return fmt.Errorf("node[%d]: file_name is required", i)
		}
	}

	return nil
}

// Execute 执行批量部署任务
func (e *BatchDeployNodeExecutor) Execute(ctx context.Context, task *asynctaskmodel.AsyncTask) error {
	log.InfoContextf(ctx, "Starting batch deploy node task: %s", task.TaskID)

	// 解析请求参数
	var request BatchDeployNodeRequest
	if err := json.Unmarshal([]byte(task.RequestParams), &request); err != nil {
		errorMsg := fmt.Sprintf("failed to parse request params: %v", err)
		e.asyncTaskService.CompleteTask(ctx, task.TaskID, asynctaskmodel.TaskStatusFailed, nil, errorMsg)
		return fmt.Errorf(errorMsg)
	}

	// 创建任务详情
	var taskItems []asynctasklogic.TaskItem
	for _, node := range request.Nodes {
		taskItems = append(taskItems, asynctasklogic.TaskItem{
			ItemID:   node.NodeID,
			ItemName: fmt.Sprintf("Deploy %s to Node %s", node.FileName, node.NodeID),
		})
	}

	if err := e.asyncTaskService.BatchCreateTaskDetails(ctx, task.TaskID, taskItems); err != nil {
		log.ErrorContextf(ctx, "Failed to create task details: %v", err)
	}

	// 将节点部署任务加入队列
	enqueuedCount := 0
	failedToEnqueueCount := 0

	for _, node := range request.Nodes {
		// 更新任务详情状态为处理中
		e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, node.NodeID,
			asynctaskmodel.TaskDetailStatusProcessing, "")

		// 将节点部署任务加入队列（实际部署将由Worker异步执行）
		err := e.scfNodeService.DeployToNode(ctx, node.NodeID, node.ZipFileBase64, node.FileName, task.TaskID)
		if err != nil {
			failedToEnqueueCount++
			errorMsg := fmt.Sprintf("将节点加入部署队列失败: %v", err)
			log.ErrorContextf(ctx, "Failed to enqueue node deployment for %s: %v", node.NodeID, err)

			// 如果无法加入队列，直接标记为失败
			e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, node.NodeID,
				asynctaskmodel.TaskDetailStatusFailed, errorMsg)
		} else {
			enqueuedCount++
			log.InfoContextf(ctx, "Successfully enqueued node %s for deployment; taskID:%s, file:%s", 
				node.NodeID, task.TaskID, node.FileName)
			// 注意：这里不再立即更新为成功状态，保持处理中状态
			// 实际的成功/失败状态将由NodeDeploymentWorker在部署完成后更新
		}
	}

	// 记录任务创建情况
	if failedToEnqueueCount == 0 {
		log.InfoContextf(ctx, "All nodes enqueued for deployment. Total: %d", len(request.Nodes))
	} else if enqueuedCount == 0 {
		log.ErrorContextf(ctx, "Failed to enqueue any nodes for deployment. Total: %d", len(request.Nodes))
		// 如果所有节点都无法加入队列，直接标记任务失败
		resultData := map[string]interface{}{
			"total_count":   len(request.Nodes),
			"success_count": 0,
			"failed_count":  failedToEnqueueCount,
		}
		return e.asyncTaskService.CompleteTask(ctx, task.TaskID, asynctaskmodel.TaskStatusFailed, resultData, "所有节点都无法加入部署队列")
	} else {
		log.WarnContextf(ctx, "Partially enqueued nodes for deployment. Total: %d, Enqueued: %d, Failed: %d",
			len(request.Nodes), enqueuedCount, failedToEnqueueCount)
	}

	// 任务已经提交到队列，等待Worker处理
	// 注意：这里不再调用CompleteTask，任务将保持处理中状态，直到所有Worker完成处理
	return nil
}