package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/server/internal/service/collector/logic"
	"github.com/mooyang-code/moox/server/internal/service/collector/model"
	"gorm.io/gorm"
)

// CollectorTaskConfigHandler 采集任务配置处理器
type CollectorTaskConfigHandler struct {
	service logic.CollectorTaskConfigService
}

// NewCollectorTaskConfigHandler 创建采集任务配置处理器
func NewCollectorTaskConfigHandler(db *gorm.DB) SchemaHandler {
	return &CollectorTaskConfigHandler{
		service: logic.NewCollectorTaskConfigService(db),
	}
}

// SchemaID 返回表名
func (h *CollectorTaskConfigHandler) SchemaID() string {
	return model.CollectorTaskConfigTable
}

// GetHandle 处理GET请求
func (h *CollectorTaskConfigHandler) GetHandle(ctx context.Context, params map[string]string) (*APIResponse, error) {
	// 支持根据task_id查询单个配置
	if taskID, ok := params["task_id"]; ok && taskID != "" {
		config, err := h.service.GetTaskConfig(ctx, taskID)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get task config: %w", err)
		}

		if config == nil {
			return &APIResponse{
				Code: 404,
				Data: []interface{}{},
			}, nil
		}

		return &APIResponse{
			Code: 200,
			Data: []interface{}{config},
		}, nil
	}

	// 支持按节点查询配置
	if nodeID, ok := params["node_id"]; ok && nodeID != "" {
		configs, err := h.service.GetTaskConfigsByNode(ctx, nodeID)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get task configs by node: %w", err)
		}

		data := make([]interface{}, 0, len(configs))
		for _, config := range configs {
			data = append(data, config)
		}

		return &APIResponse{
			Code: 200,
			Data: data,
		}, nil
	}

	// 支持按任务类型查询
	if taskType, ok := params["task_type"]; ok && taskType != "" {
		configs, err := h.service.GetTaskConfigsByType(ctx, taskType)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get task configs by type: %w", err)
		}

		data := make([]interface{}, 0, len(configs))
		for _, config := range configs {
			data = append(data, config)
		}

		return &APIResponse{
			Code: 200,
			Data: data,
		}, nil
	}

	// 支持查询启用的配置
	if enabled, ok := params["enabled"]; ok && enabled == model.EnabledTrue {
		configs, err := h.service.GetEnabledTaskConfigs(ctx)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get enabled task configs: %w", err)
		}

		data := make([]interface{}, 0, len(configs))
		for _, config := range configs {
			data = append(data, config)
		}

		return &APIResponse{
			Code: 200,
			Data: data,
		}, nil
	}

	// 获取配置列表（支持project_id和dataset_id过滤）
	projectID := params["project_id"]
	datasetID := params["dataset_id"]
	configs, err := h.service.GetTaskConfigList(ctx, projectID, datasetID)
	if err != nil {
		return &APIResponse{
			Code: 500,
			Data: []interface{}{},
		}, fmt.Errorf("failed to get task config list: %w", err)
	}

	data := make([]interface{}, 0, len(configs))
	for _, config := range configs {
		data = append(data, config)
	}

	return &APIResponse{
		Code: 200,
		Data: data,
	}, nil
}

// PostHandle 处理POST请求
func (h *CollectorTaskConfigHandler) PostHandle(ctx context.Context, params map[string]string) (*APIResponse, error) {
	action := params["_action"]

	switch action {
	case "create":
		// 创建新配置
		config := h.parseTaskConfig(params)

		if err := h.service.CreateTaskConfig(ctx, config); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to create task config: %w", err)
		}

		return &APIResponse{
			Code: 200,
			Data: []interface{}{config},
		}, nil

	case "update":
		// 更新配置
		config := h.parseTaskConfig(params)

		if err := h.service.UpdateTaskConfig(ctx, config); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to update task config: %w", err)
		}

		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil

	case "delete":
		// 删除配置
		taskID := params["task_id"]
		if err := h.service.RemoveTaskConfig(ctx, taskID); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to delete task config: %w", err)
		}

		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil

	case "batch_update_enabled":
		// 批量更新启用状态
		taskIDs := strings.Split(params["task_ids"], ",")
		enabled := params["enabled"]

		if err := h.service.BatchUpdateEnabled(ctx, taskIDs, enabled); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to batch update enabled status: %w", err)
		}

		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil

	case "update_dispatch_result":
		// 更新分发结果
		taskID := params["task_id"]
		result := params["result"]

		if err := h.service.UpdateDispatchResult(ctx, taskID, result); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to update dispatch result: %w", err)
		}

		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil

	default:
		return &APIResponse{
			Code: 400,
			Data: []interface{}{},
		}, fmt.Errorf("invalid action: %s", action)
	}
}

// parseTaskConfig 解析任务配置参数
func (h *CollectorTaskConfigHandler) parseTaskConfig(params map[string]string) *model.CollectorTaskConfig {
	config := &model.CollectorTaskConfig{
		TaskID:              params["task_id"],
		ProjectID:           params["project_id"],
		DatasetID:           params["dataset_id"],
		TaskType:            params["task_type"],
		CollectorType:       params["collector_type"],
		SourceName:          params["source_name"],
		AssignmentType:      params["assignment_type"],
		AssignedNodes:       params["assigned_nodes"],
		NodePattern:         params["node_pattern"],
		LoadBalanceStrategy: params["load_balance_strategy"],
		TargetObjects:       params["target_objects"],
		ObjectPattern:       params["object_pattern"],
		ForceObjects:        params["force_objects"],
		CollectParams:       params["collect_params"],
		ScheduleConfig:      params["schedule_config"],
		Enabled:             params["enabled"],
	}

	// 解析优先级
	if priorityStr, ok := params["priority"]; ok {
		priority, err := strconv.Atoi(priorityStr)
		if err == nil {
			config.Priority = priority
		}
	}

	return config
}
