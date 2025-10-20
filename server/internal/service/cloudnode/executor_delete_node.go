package cloudnode

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"

	"trpc.group/trpc-go/trpc-go/log"
)

// DeleteNodeExecutor 单节点删除执行器
type DeleteNodeExecutor struct {
	cloudNodeService Service
	heartbeatService HeartbeatService
}

// NewDeleteNodeExecutor 创建单节点删除执行器
func NewDeleteNodeExecutor(cloudNodeService Service, heartbeatService HeartbeatService) *DeleteNodeExecutor {
	return &DeleteNodeExecutor{
		cloudNodeService: cloudNodeService,
		heartbeatService: heartbeatService,
	}
}

// NodeDeleteItem 节点删除项
type NodeDeleteItem struct {
	NodeID string `json:"node_id"`
}

// Name 返回执行器外显名称
func (e *DeleteNodeExecutor) Name() string {
	return "删除节点"
}

// Type 返回执行器类型
func (e *DeleteNodeExecutor) Type() string {
	return asynctask.TaskTypeDeleteNode
}

// Execute 执行单个节点删除任务 - 新TaskHandler签名
// 返回: resultData (JSON字符串), error
func (e *DeleteNodeExecutor) Execute(ctx context.Context, taskID string, requestParams string) (string, error) {
	// 解析请求参数
	var deleteItem NodeDeleteItem
	if err := json.Unmarshal([]byte(requestParams), &deleteItem); err != nil {
		return "", fmt.Errorf("failed to parse task data: %w", err)
	}

	log.InfoContextf(ctx, "[DeleteNodeExecutor] Deleting node: TaskID=%s, NodeID=%s",
		taskID, deleteItem.NodeID)

	// 先从心跳服务中注销节点（避免心跳服务继续探测已删除的节点）
	if e.heartbeatService != nil {
		if err := e.heartbeatService.UnregisterHeartbeatNode(ctx, deleteItem.NodeID, "SCF"); err != nil {
			log.WarnContextf(ctx, "[DeleteNodeExecutor] Failed to unregister node from heartbeat service: %v", err)
			// 心跳注销失败不影响节点删除流程，仅记录警告
		} else {
			log.InfoContextf(ctx, "[DeleteNodeExecutor] Node unregistered from heartbeat service: %s", deleteItem.NodeID)
		}
	}

	// 调用云厂商API删除节点
	err := e.cloudNodeService.DeleteNode(ctx, deleteItem.NodeID)
	if err != nil {
		log.ErrorContextf(ctx, "[DeleteNodeExecutor] Failed to delete node %s: %v", deleteItem.NodeID, err)
		return "", fmt.Errorf("删除节点失败: %w", err)
	}

	// 从数据库中删除节点记录
	if err := e.deleteNodeFromDB(ctx, deleteItem.NodeID); err != nil {
		log.ErrorContextf(ctx, "[DeleteNodeExecutor] Failed to delete node from database: %v", err)
		return "", fmt.Errorf("从数据库删除节点失败: %w", err)
	}

	log.InfoContextf(ctx, "[DeleteNodeExecutor] Node deleted successfully: NodeID=%s, TaskID=%s",
		deleteItem.NodeID, taskID)

	// 任务成功完成，返回JSON结果
	resultData := map[string]interface{}{
		"node_id": deleteItem.NodeID,
	}
	resultJSON, err := json.Marshal(resultData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal result data: %w", err)
	}
	return string(resultJSON), nil
}

// deleteNodeFromDB 从数据库中删除节点
func (e *DeleteNodeExecutor) deleteNodeFromDB(ctx context.Context, nodeID string) error {
	return e.cloudNodeService.DeleteNodeFromDB(ctx, nodeID)
}
