package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/server/internal/service/collector/logic"
	"github.com/mooyang-code/moox/server/internal/service/collector/model"
	"gorm.io/gorm"
)

// CollectorTaskInstanceHandler 采集任务实例处理器
type CollectorTaskInstanceHandler struct {
	service logic.CollectorTaskInstanceService
}

// NewCollectorTaskInstanceHandler 创建采集任务实例处理器
func NewCollectorTaskInstanceHandler(db *gorm.DB) SchemaHandler {
	return &CollectorTaskInstanceHandler{
		service: logic.NewCollectorTaskInstanceService(db),
	}
}

// SchemaID 返回表名
func (h *CollectorTaskInstanceHandler) SchemaID() string {
	return model.CollectorTaskInstanceTable
}

// GetHandle 处理GET请求
func (h *CollectorTaskInstanceHandler) GetHandle(ctx context.Context, params map[string]string) (*APIResponse, error) {
	// 支持根据instance_id查询单个实例
	if instanceID, ok := params["instance_id"]; ok && instanceID != "" {
		instance, err := h.service.GetTaskInstance(ctx, instanceID)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get task instance: %w", err)
		}
		
		if instance == nil {
			return &APIResponse{
				Code: 404,
				Data: []interface{}{},
			}, nil
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{instance},
		}, nil
	}
	
	// 支持按节点查询实例
	if nodeID, ok := params["node_id"]; ok && nodeID != "" {
		var status []int
		if statusStr, ok := params["status"]; ok && statusStr != "" {
			statusList := strings.Split(statusStr, ",")
			for _, s := range statusList {
				if val, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
					status = append(status, val)
				}
			}
		}
		
		instances, err := h.service.GetTaskInstancesByNode(ctx, nodeID, status)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get task instances by node: %w", err)
		}
		
		data := make([]interface{}, 0, len(instances))
		for _, instance := range instances {
			data = append(data, instance)
		}
		
		return &APIResponse{
			Code: 200,
			Data: data,
		}, nil
	}
	
	// 支持按任务ID查询实例历史
	if taskID, ok := params["task_id"]; ok && taskID != "" {
		limit := 100 // 默认限制
		if limitStr, ok := params["limit"]; ok {
			if val, err := strconv.Atoi(limitStr); err == nil && val > 0 {
				limit = val
			}
		}
		
		instances, err := h.service.GetTaskInstancesByTask(ctx, taskID, limit)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get task instances by task: %w", err)
		}
		
		data := make([]interface{}, 0, len(instances))
		for _, instance := range instances {
			data = append(data, instance)
		}
		
		return &APIResponse{
			Code: 200,
			Data: data,
		}, nil
	}
	
	// 查询待执行的实例
	if pending, ok := params["pending"]; ok && pending == "true" {
		nodeID := params["filter_node_id"] // 可选的节点过滤
		instances, err := h.service.GetPendingInstances(ctx, nodeID)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get pending instances: %w", err)
		}
		
		data := make([]interface{}, 0, len(instances))
		for _, instance := range instances {
			data = append(data, instance)
		}
		
		return &APIResponse{
			Code: 200,
			Data: data,
		}, nil
	}
	
	// 查询正在执行的实例
	if running, ok := params["running"]; ok && running == "true" {
		nodeID := params["filter_node_id"] // 可选的节点过滤
		instances, err := h.service.GetRunningInstances(ctx, nodeID)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get running instances: %w", err)
		}
		
		data := make([]interface{}, 0, len(instances))
		for _, instance := range instances {
			data = append(data, instance)
		}
		
		return &APIResponse{
			Code: 200,
			Data: data,
		}, nil
	}
	
	// 查询最近的实例
	hours := 24 // 默认24小时
	if hoursStr, ok := params["hours"]; ok {
		if val, err := strconv.Atoi(hoursStr); err == nil && val > 0 {
			hours = val
		}
	}
	
	instances, err := h.service.GetRecentInstances(ctx, hours)
	if err != nil {
		return &APIResponse{
			Code: 500,
			Data: []interface{}{},
		}, fmt.Errorf("failed to get recent instances: %w", err)
	}
	
	data := make([]interface{}, 0, len(instances))
	for _, instance := range instances {
		data = append(data, instance)
	}
	
	return &APIResponse{
		Code: 200,
		Data: data,
	}, nil
}

// PostHandle 处理POST请求
func (h *CollectorTaskInstanceHandler) PostHandle(ctx context.Context, params map[string]string) (*APIResponse, error) {
	action := params["_action"]
	
	switch action {
	case "create":
		// 创建单个实例
		instance := h.parseTaskInstance(params)
		
		if err := h.service.CreateTaskInstance(ctx, instance); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to create task instance: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{instance},
		}, nil
		
	case "batch_create":
		// 批量创建实例
		taskID := params["task_id"]
		assignmentsStr := params["node_assignments"] // JSON格式：{"node1":["obj1","obj2"],"node2":["obj3"]}
		
		var nodeAssignments map[string][]string
		if err := json.Unmarshal([]byte(assignmentsStr), &nodeAssignments); err != nil {
			return &APIResponse{
				Code: 400,
				Data: []interface{}{},
			}, fmt.Errorf("invalid node_assignments format: %w", err)
		}
		
		if err := h.service.BatchCreateTaskInstances(ctx, taskID, nodeAssignments); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to batch create instances: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	case "start":
		// 开始执行实例
		instanceID := params["instance_id"]
		
		if err := h.service.StartInstance(ctx, instanceID); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to start instance: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	case "complete":
		// 完成实例执行
		instanceID := params["instance_id"]
		success := params["success"] == "true"
		result := params["result"]
		
		if err := h.service.CompleteInstance(ctx, instanceID, success, result); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to complete instance: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	case "update_status":
		// 更新实例状态
		instanceID := params["instance_id"]
		statusStr := params["status"]
		result := params["result"]
		
		status, err := strconv.Atoi(statusStr)
		if err != nil {
			return &APIResponse{
				Code: 400,
				Data: []interface{}{},
			}, fmt.Errorf("invalid status: %s", statusStr)
		}
		
		if err := h.service.UpdateInstanceStatus(ctx, instanceID, status, result); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to update instance status: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	case "retry":
		// 重试失败的实例
		instanceID := params["instance_id"]
		
		if err := h.service.RetryFailedInstance(ctx, instanceID); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to retry instance: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	case "cleanup":
		// 清理旧实例
		days := 30 // 默认30天
		if daysStr, ok := params["days"]; ok {
			if val, err := strconv.Atoi(daysStr); err == nil && val > 0 {
				days = val
			}
		}
		
		if err := h.service.CleanupOldInstances(ctx, days); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to cleanup old instances: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	case "cancel":
		// 取消任务实例
		instanceID := params["instance_id"]
		
		if err := h.service.CancelInstance(ctx, instanceID); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to cancel instance: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	case "get_logs":
		// 获取任务实例日志
		instanceID := params["instance_id"]
		
		logs, err := h.service.GetInstanceLogs(ctx, instanceID)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get instance logs: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: logs, // 直接返回日志内容字符串
		}, nil
		
	default:
		return &APIResponse{
			Code: 400,
			Data: []interface{}{},
		}, fmt.Errorf("invalid action: %s", action)
	}
}

// parseTaskInstance 解析任务实例参数
func (h *CollectorTaskInstanceHandler) parseTaskInstance(params map[string]string) *model.CollectorTaskInstance {
	instance := &model.CollectorTaskInstance{
		InstanceID:      params["instance_id"],
		TaskID:          params["task_id"],
		ProjectID:       params["project_id"],
		DatasetID:       params["dataset_id"],
		NodeID:          params["node_id"],
		TargetObjects:   params["target_objects"],
		ExecutionParams: params["execution_params"],
		Result:          params["result"],
	}
	
	// 解析状态
	if statusStr, ok := params["status"]; ok {
		status, err := strconv.Atoi(statusStr)
		if err == nil {
			instance.Status = status
		}
	}
	
	return instance
}