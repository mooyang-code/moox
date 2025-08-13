package logic

import (
	"context"
	"encoding/json"
	"fmt"

	asynctasklogic "github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	asynctaskmodel "github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	cloudnodelogic "github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// BatchCreateNodeRequest 批量创建节点请求
type BatchCreateNodeRequest struct {
	Nodes []NodeCreateItem `json:"nodes"`
}

// NodeCreateItem 节点创建项
type NodeCreateItem struct {
	CloudAccountID      string `json:"cloud_account_id"`
	NodeType            string `json:"node_type"`
	Region              string `json:"region"`
	IPAddress           string `json:"ip_address"`
	Version             string `json:"version"`
	SupportedCollectors string `json:"supported_collectors"`
	Capacity            string `json:"capacity"`
	Metadata            string `json:"metadata"`
}

// BatchCreateNodeExecutor 批量创建节点执行器
type BatchCreateNodeExecutor struct {
	scfNodeService   cloudnodelogic.SCFNodeService
	asyncTaskService asynctasklogic.AsyncTaskService
	db               *gorm.DB
}

// NewBatchCreateNodeExecutor 创建批量创建节点执行器
func NewBatchCreateNodeExecutor(db *gorm.DB, scfNodeService cloudnodelogic.SCFNodeService, asyncTaskService asynctasklogic.AsyncTaskService) *BatchCreateNodeExecutor {
	return &BatchCreateNodeExecutor{
		scfNodeService:   scfNodeService,
		asyncTaskService: asyncTaskService,
		db:               db,
	}
}

// GetTaskType 返回任务类型
func (e *BatchCreateNodeExecutor) GetTaskType() string {
	return asynctaskmodel.TaskTypeBatchCreateNode
}

// ValidateRequest 验证请求
func (e *BatchCreateNodeExecutor) ValidateRequest(taskData string) error {
	var request BatchCreateNodeRequest
	if err := json.Unmarshal([]byte(taskData), &request); err != nil {
		return fmt.Errorf("invalid request format: %w", err)
	}

	if len(request.Nodes) == 0 {
		return fmt.Errorf("no nodes to create")
	}

	return nil
}

// Execute 执行批量创建节点任务
func (e *BatchCreateNodeExecutor) Execute(ctx context.Context, task *asynctaskmodel.AsyncTask) error {
	// 解析请求参数
	var request BatchCreateNodeRequest
	if err := json.Unmarshal([]byte(task.RequestParams), &request); err != nil {
		return fmt.Errorf("failed to parse task data: %w", err)
	}
	log.InfoContextf(ctx, "BatchCreateNodeExecutor Execute : total nodes=%d; TaskID=%s", len(request.Nodes), task.TaskID)

	// 批量创建任务详情记录
	taskItems := make([]asynctasklogic.TaskItem, len(request.Nodes))
	for i := range request.Nodes {
		itemID := fmt.Sprintf("node_%d", i+1)
		taskItems[i] = asynctasklogic.TaskItem{
			ItemID:   itemID,
			ItemName: fmt.Sprintf("Node %d", i+1),
		}
	}
	if err := e.asyncTaskService.BatchCreateTaskDetails(ctx, task.TaskID, taskItems); err != nil {
		log.ErrorContextf(ctx, "Failed to create task details: %v", err)
		return fmt.Errorf("failed to create task details: %w", err)
	}

	// 处理每个节点
	successCount := 0
	failedCount := 0
	for i, nodeData := range request.Nodes {
		itemID := fmt.Sprintf("node_%d", i+1)

		// 创建任务详情记录
		e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, itemID, asynctaskmodel.TaskDetailStatusProcessing, "")

		// 准备节点数据
		node := &cloudnodemodel.SCFNode{
			CloudAccountID:      nodeData.CloudAccountID,
			NodeType:            nodeData.NodeType,
			Region:              nodeData.Region,
			IPAddress:           nodeData.IPAddress,
			Version:             nodeData.Version,
			SupportedCollectors: nodeData.SupportedCollectors,
			Capacity:            nodeData.Capacity,
			Metadata:            nodeData.Metadata,
			Status:              cloudnodemodel.NodeStatusMaintenance, // 初始状态为维护中
		}

		// 调用服务创建节点，并传递任务ID
		registeredNode, err := e.scfNodeService.CreateNode(ctx, node, task.TaskID, itemID)
		if err != nil {
			failedCount++
			errMsg := fmt.Sprintf("failed to prepare node: %v", err)
			log.ErrorContextf(ctx, "Failed to prepare node %s: %v", itemID, err)
			e.asyncTaskService.UpdateTaskDetailStatus(ctx, task.TaskID, itemID, asynctaskmodel.TaskDetailStatusFailed, errMsg)
		} else {
			// 节点准备成功，但实际创建将由worker异步完成
			// 这里我们将任务状态保持为处理中，等待worker更新最终状态
			successCount++
			log.InfoContextf(ctx, "Node %s prepared successfully, will be created async by worker. taskID:%s",
				registeredNode.NodeID, task.TaskID)
		}
	}

	// 更新任务汇总信息
	summary := fmt.Sprintf("Total: %d, Prepared: %d, Failed: %d",
		len(request.Nodes), successCount, failedCount)

	// 如果全部失败，则任务失败
	if failedCount == len(request.Nodes) {
		return fmt.Errorf("all nodes failed to prepare: %s", summary)
	}

	log.InfoContextf(ctx, "Batch node creation completed: %s", summary)
	return nil
}
