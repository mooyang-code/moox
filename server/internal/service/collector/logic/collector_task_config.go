package logic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/collector/dao"
	"github.com/mooyang-code/moox/server/internal/service/collector/model"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// CollectorTaskConfigService 采集任务配置服务接口
type CollectorTaskConfigService interface {
	// 基础CRUD
	GetTaskConfigList(ctx context.Context, projectID, datasetID string) ([]*model.CollectorTaskConfig, error)
	GetTaskConfig(ctx context.Context, taskID string) (*model.CollectorTaskConfig, error)
	CreateTaskConfig(ctx context.Context, config *model.CollectorTaskConfig) error
	UpdateTaskConfig(ctx context.Context, config *model.CollectorTaskConfig) error
	RemoveTaskConfig(ctx context.Context, taskID string) error

	// 查询功能
	GetEnabledTaskConfigs(ctx context.Context) ([]*model.CollectorTaskConfig, error)
	GetTaskConfigsByType(ctx context.Context, taskType string) ([]*model.CollectorTaskConfig, error)
	GetTaskConfigsByNode(ctx context.Context, nodeID string) ([]*model.CollectorTaskConfig, error)

	// 批量操作
	BatchUpdateEnabled(ctx context.Context, taskIDs []string, enabled string) error
	UpdateDispatchResult(ctx context.Context, taskID string, result string) error

	// 配置同步
	SyncTaskConfigToNode(ctx context.Context, taskID string, nodeID string) error
}

type collectorTaskConfigServiceImpl struct {
	taskConfigDAO dao.CollectorTaskConfigDAO
	nodeDAO       cloudnodedao.SCFNodeDAO
	cloudProvider provider.CloudProvider
}

// NewCollectorTaskConfigService 创建采集任务配置服务实例
func NewCollectorTaskConfigService(db *gorm.DB) CollectorTaskConfigService {
	return &collectorTaskConfigServiceImpl{
		taskConfigDAO: dao.NewCollectorTaskConfigDAO(db),
		nodeDAO:       cloudnodedao.NewSCFNodeDAO(db),
		cloudProvider: nil, // 暂时设置为nil，后续可以通过SetCloudProvider方法设置
	}
}

// NewCollectorTaskConfigServiceWithProvider 创建带云提供商的采集任务配置服务实例
func NewCollectorTaskConfigServiceWithProvider(db *gorm.DB, provider provider.CloudProvider) CollectorTaskConfigService {
	return &collectorTaskConfigServiceImpl{
		taskConfigDAO: dao.NewCollectorTaskConfigDAO(db),
		nodeDAO:       cloudnodedao.NewSCFNodeDAO(db),
		cloudProvider: provider,
	}
}

// GetTaskConfigList 获取任务配置列表
func (s *collectorTaskConfigServiceImpl) GetTaskConfigList(ctx context.Context, projectID, datasetID string) ([]*model.CollectorTaskConfig, error) {
	configs, err := s.taskConfigDAO.GetTaskConfigList(ctx, projectID, datasetID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task config list: %w", err)
	}
	return configs, nil
}

// GetTaskConfig 获取单个任务配置
func (s *collectorTaskConfigServiceImpl) GetTaskConfig(ctx context.Context, taskID string) (*model.CollectorTaskConfig, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task ID is required")
	}

	config, err := s.taskConfigDAO.GetTaskConfig(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task config: %w", err)
	}

	if config == nil {
		return nil, fmt.Errorf("task config not found")
	}

	return config, nil
}

// CreateTaskConfig 创建任务配置
func (s *collectorTaskConfigServiceImpl) CreateTaskConfig(ctx context.Context, config *model.CollectorTaskConfig) error {
	// 验证必填字段
	if err := s.validateTaskConfig(config); err != nil {
		return err
	}

	// 检查任务ID是否已存在
	if config.TaskID != "" {
		existing, err := s.taskConfigDAO.GetTaskConfig(ctx, config.TaskID)
		if err != nil {
			return fmt.Errorf("failed to check existing task: %w", err)
		}
		if existing != nil {
			return fmt.Errorf("task ID already exists")
		}
	} else {
		// 自动生成任务ID
		config.TaskID = fmt.Sprintf("task-%s-%s-%s-%d",
			config.ProjectID, config.DatasetID, config.CollectorType, time.Now().Unix())
	}

	// 设置默认值
	config.Invalid = model.InvalidNo
	if config.Enabled == "" {
		config.Enabled = model.EnabledTrue
	}
	if config.AssignmentType == "" {
		config.AssignmentType = model.AssignmentTypeAuto
	}
	if config.LoadBalanceStrategy == "" {
		config.LoadBalanceStrategy = model.LoadBalanceRoundRobin
	}

	// 验证分配配置
	if err := s.validateAssignmentConfig(ctx, config); err != nil {
		return err
	}

	// 验证JSON字段
	if err := s.validateJSONFields(config); err != nil {
		return err
	}

	if err := s.taskConfigDAO.CreateTaskConfig(ctx, config); err != nil {
		return fmt.Errorf("failed to create task config: %w", err)
	}

	return nil
}

// UpdateTaskConfig 更新任务配置
func (s *collectorTaskConfigServiceImpl) UpdateTaskConfig(ctx context.Context, config *model.CollectorTaskConfig) error {
	if config.TaskID == "" {
		return fmt.Errorf("task ID is required")
	}

	// 检查任务是否存在
	existing, err := s.taskConfigDAO.GetTaskConfig(ctx, config.TaskID)
	if err != nil {
		return fmt.Errorf("failed to check existing task: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("task config not found")
	}

	// 验证必填字段
	if err := s.validateTaskConfig(config); err != nil {
		return err
	}

	// 验证分配配置
	if err := s.validateAssignmentConfig(ctx, config); err != nil {
		return err
	}

	// 验证JSON字段
	if err := s.validateJSONFields(config); err != nil {
		return err
	}

	if err := s.taskConfigDAO.UpdateTaskConfig(ctx, config); err != nil {
		return fmt.Errorf("failed to update task config: %w", err)
	}

	return nil
}

// RemoveTaskConfig 删除任务配置
func (s *collectorTaskConfigServiceImpl) RemoveTaskConfig(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("task ID is required")
	}

	// TODO: 检查是否有正在运行的任务实例

	if err := s.taskConfigDAO.DeleteTaskConfig(ctx, taskID); err != nil {
		return fmt.Errorf("failed to remove task config: %w", err)
	}

	return nil
}

// GetEnabledTaskConfigs 获取所有启用的任务配置
func (s *collectorTaskConfigServiceImpl) GetEnabledTaskConfigs(ctx context.Context) ([]*model.CollectorTaskConfig, error) {
	configs, err := s.taskConfigDAO.GetEnabledTaskConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get enabled task configs: %w", err)
	}
	return configs, nil
}

// GetTaskConfigsByType 根据任务类型获取配置
func (s *collectorTaskConfigServiceImpl) GetTaskConfigsByType(ctx context.Context, taskType string) ([]*model.CollectorTaskConfig, error) {
	if taskType == "" {
		return nil, fmt.Errorf("task type is required")
	}

	configs, err := s.taskConfigDAO.GetTaskConfigsByType(ctx, taskType)
	if err != nil {
		return nil, fmt.Errorf("failed to get task configs by type: %w", err)
	}
	return configs, nil
}

// GetTaskConfigsByNode 获取节点的任务配置
func (s *collectorTaskConfigServiceImpl) GetTaskConfigsByNode(ctx context.Context, nodeID string) ([]*model.CollectorTaskConfig, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("node ID is required")
	}

	// 检查节点是否存在
	node, err := s.nodeDAO.GetSCFNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to check node: %w", err)
	}
	if node == nil {
		return nil, fmt.Errorf("node not found")
	}

	configs, err := s.taskConfigDAO.GetTaskConfigsByNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task configs by node: %w", err)
	}
	return configs, nil
}

// BatchUpdateEnabled 批量更新启用状态
func (s *collectorTaskConfigServiceImpl) BatchUpdateEnabled(ctx context.Context, taskIDs []string, enabled string) error {
	if len(taskIDs) == 0 {
		return fmt.Errorf("task IDs are required")
	}

	if enabled != model.EnabledTrue && enabled != model.EnabledFalse {
		return fmt.Errorf("invalid enabled value: %s", enabled)
	}

	if err := s.taskConfigDAO.BatchUpdateEnabled(ctx, taskIDs, enabled); err != nil {
		return fmt.Errorf("failed to batch update enabled status: %w", err)
	}

	return nil
}

// UpdateDispatchResult 更新任务分发结果
func (s *collectorTaskConfigServiceImpl) UpdateDispatchResult(ctx context.Context, taskID string, result string) error {
	if taskID == "" {
		return fmt.Errorf("task ID is required")
	}

	if err := s.taskConfigDAO.UpdateDispatchResult(ctx, taskID, result); err != nil {
		return fmt.Errorf("failed to update dispatch result: %w", err)
	}

	return nil
}

// validateTaskConfig 验证任务配置必填字段
func (s *collectorTaskConfigServiceImpl) validateTaskConfig(config *model.CollectorTaskConfig) error {
	if config.ProjectID == "" {
		return fmt.Errorf("project ID is required")
	}
	if config.DatasetID == "" {
		return fmt.Errorf("dataset ID is required")
	}
	if config.TaskType == "" {
		return fmt.Errorf("task type is required")
	}
	if config.CollectorType == "" {
		return fmt.Errorf("collector type is required")
	}
	if config.SourceName == "" {
		return fmt.Errorf("source name is required")
	}

	// 验证任务类型
	if config.TaskType != model.TaskTypeObjectList && config.TaskType != model.TaskTypeDataCollect {
		return fmt.Errorf("invalid task type: %s", config.TaskType)
	}

	// 验证分配类型
	validAssignmentTypes := []string{
		model.AssignmentTypeAuto,
		model.AssignmentTypeFixed,
		model.AssignmentTypePattern,
	}
	valid := false
	for _, t := range validAssignmentTypes {
		if config.AssignmentType == t {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid assignment type: %s", config.AssignmentType)
	}

	return nil
}

// validateAssignmentConfig 验证任务分配配置
func (s *collectorTaskConfigServiceImpl) validateAssignmentConfig(ctx context.Context, config *model.CollectorTaskConfig) error {
	switch config.AssignmentType {
	case model.AssignmentTypeFixed:
		// 固定分配必须指定节点
		if config.AssignedNodes == "" || config.AssignedNodes == "[]" {
			return fmt.Errorf("assigned nodes are required for fixed assignment type")
		}

		// 验证节点列表是否为有效的JSON数组
		var nodes []string
		if err := json.Unmarshal([]byte(config.AssignedNodes), &nodes); err != nil {
			return fmt.Errorf("invalid assigned nodes format: %w", err)
		}

		// 检查节点是否存在
		for _, nodeID := range nodes {
			node, err := s.nodeDAO.GetSCFNode(ctx, nodeID)
			if err != nil {
				return fmt.Errorf("failed to check node %s: %w", nodeID, err)
			}
			if node == nil {
				return fmt.Errorf("node %s not found", nodeID)
			}
		}

	case model.AssignmentTypePattern:
		// 模式匹配必须指定匹配模式
		if config.NodePattern == "" {
			return fmt.Errorf("node pattern is required for pattern assignment type")
		}

		// 验证是否是有效的通配符模式
		if !strings.Contains(config.NodePattern, "*") && !strings.Contains(config.NodePattern, "?") {
			return fmt.Errorf("node pattern must contain wildcard characters (* or ?)")
		}
	}

	// 验证负载均衡策略
	validStrategies := []string{
		model.LoadBalanceRoundRobin,
		model.LoadBalanceLeastLoad,
		model.LoadBalanceRandom,
	}
	valid := false
	for _, s := range validStrategies {
		if config.LoadBalanceStrategy == s {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid load balance strategy: %s", config.LoadBalanceStrategy)
	}

	return nil
}

// validateJSONFields 验证JSON格式字段
func (s *collectorTaskConfigServiceImpl) validateJSONFields(config *model.CollectorTaskConfig) error {
	// 验证AssignedNodes
	if config.AssignedNodes != "" {
		var nodes []string
		if err := json.Unmarshal([]byte(config.AssignedNodes), &nodes); err != nil {
			return fmt.Errorf("invalid assigned_nodes format: %w", err)
		}
	}

	// 验证TargetObjects
	if config.TargetObjects != "" {
		var objects []interface{}
		if err := json.Unmarshal([]byte(config.TargetObjects), &objects); err != nil {
			return fmt.Errorf("invalid target_objects format: %w", err)
		}
	}

	// 验证ForceObjects
	if config.ForceObjects != "" {
		var force map[string][]string
		if err := json.Unmarshal([]byte(config.ForceObjects), &force); err != nil {
			return fmt.Errorf("invalid force_objects format: %w", err)
		}
	}

	// 验证CollectParams
	if config.CollectParams != "" {
		var params map[string]interface{}
		if err := json.Unmarshal([]byte(config.CollectParams), &params); err != nil {
			return fmt.Errorf("invalid collect_params format: %w", err)
		}
	}

	// 验证ScheduleConfig
	if config.ScheduleConfig != "" {
		var schedule map[string]interface{}
		if err := json.Unmarshal([]byte(config.ScheduleConfig), &schedule); err != nil {
			return fmt.Errorf("invalid schedule_config format: %w", err)
		}
	}

	// 设置默认值
	if config.AssignedNodes == "" {
		config.AssignedNodes = "[]"
	}
	if config.TargetObjects == "" {
		config.TargetObjects = "[]"
	}
	if config.ForceObjects == "" {
		config.ForceObjects = "{}"
	}
	if config.CollectParams == "" {
		config.CollectParams = "{}"
	}
	if config.ScheduleConfig == "" {
		config.ScheduleConfig = "{}"
	}

	return nil
}

// TaskConfigImportEvent 任务配置导入事件结构
type TaskConfigImportEvent struct {
	Action     string                     `json:"action"`      // 动作类型：task_config_import
	TaskConfig *model.CollectorTaskConfig `json:"task_config"` // 任务配置详情
}

// SyncTaskConfigToNode 同步任务配置到指定节点
func (s *collectorTaskConfigServiceImpl) SyncTaskConfigToNode(ctx context.Context, taskID string, nodeID string) error {
	// 获取任务配置
	taskConfig, err := s.GetTaskConfig(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task config: %w", err)
	}
	if taskConfig == nil {
		return fmt.Errorf("task config not found: %s", taskID)
	}

	// 获取节点信息
	node, err := s.nodeDAO.GetSCFNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}
	if node == nil {
		return fmt.Errorf("node not found: %s", nodeID)
	}

	// 检查云提供商是否配置
	if s.cloudProvider == nil {
		return fmt.Errorf("cloud provider not configured")
	}

	log.InfoContextf(ctx, "Syncing task config %s to node %s", taskID, nodeID)

	// 构建导入事件
	event := &TaskConfigImportEvent{
		Action:     "taskConfigImport",
		TaskConfig: taskConfig,
	}

	// 调用云函数
	invokeReq := &provider.InvokeFunctionRequest{
		FunctionName: node.NodeID, // 使用NodeID作为函数名
		Namespace:    node.Namespace,
		EventData:    event,
		InvokeType:   provider.InvokeTypeSync, // 同步调用
	}

	resp, err := s.cloudProvider.InvokeFunction(ctx, invokeReq)
	if err != nil {
		return fmt.Errorf("failed to invoke cloud function: %w", err)
	}

	// 检查响应
	if resp.StatusCode != 0 && resp.StatusCode != 200 {
		log.ErrorContextf(ctx, "Cloud function returned error - StatusCode: %d, ErrorMessage: %s, ErrorType: %s",
			resp.StatusCode, resp.ErrorMessage, resp.ErrorType)
		return fmt.Errorf("cloud function error: %s (code: %d)", resp.ErrorMessage, resp.StatusCode)
	}

	// 解析响应结果
	if resp.Result != "" {
		// Result是base64编码的，需要解码
		resultBytes, err := base64.StdEncoding.DecodeString(resp.Result)
		if err != nil {
			log.WarnContextf(ctx, "Failed to decode result: %v", err)
		} else {
			log.InfoContextf(ctx, "Sync result: %s", string(resultBytes))
		}
	}

	log.InfoContextf(ctx, "Successfully synced task config %s to node %s - RequestID: %s, Duration: %dms",
		taskID, nodeID, resp.RequestID, resp.Duration)

	// 更新分发结果
	syncResult := fmt.Sprintf("Synced to %s at %s", nodeID, time.Now().Format(time.RFC3339))
	if err := s.UpdateDispatchResult(ctx, taskID, syncResult); err != nil {
		log.WarnContextf(ctx, "Failed to update dispatch result: %v", err)
	}
	return nil
}
