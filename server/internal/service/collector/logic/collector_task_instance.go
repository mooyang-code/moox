package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/collector/dao"
	"github.com/mooyang-code/moox/server/internal/service/collector/model"
	"gorm.io/gorm"
)

// CollectorTaskInstanceService 采集任务实例服务接口
type CollectorTaskInstanceService interface {
	// 创建和管理
	CreateTaskInstance(ctx context.Context, instance *model.CollectorTaskInstance) error
	BatchCreateTaskInstances(ctx context.Context, taskID string, nodeAssignments map[string][]string) error
	GetTaskInstance(ctx context.Context, instanceID string) (*model.CollectorTaskInstance, error)
	GetTaskInstanceList(ctx context.Context, nodeID string, limit, offset int) ([]*model.CollectorTaskInstance, error)
	UpdateTaskInstance(ctx context.Context, instanceID string, instance *model.CollectorTaskInstance) error
	RemoveTaskInstance(ctx context.Context, instanceID string) error
	
	// 查询功能
	GetTaskInstancesByNode(ctx context.Context, nodeID string, status []int) ([]*model.CollectorTaskInstance, error)
	GetTaskInstancesByTask(ctx context.Context, taskID string, limit int) ([]*model.CollectorTaskInstance, error)
	GetPendingInstances(ctx context.Context, nodeID string) ([]*model.CollectorTaskInstance, error)
	GetRunningInstances(ctx context.Context, nodeID string) ([]*model.CollectorTaskInstance, error)
	GetRecentInstances(ctx context.Context, hours int) ([]*model.CollectorTaskInstance, error)
	
	// 状态管理
	StartInstance(ctx context.Context, instanceID string) error
	StartTaskInstance(ctx context.Context, instanceID string) error
	StopTaskInstance(ctx context.Context, instanceID string) error
	CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error
	UpdateInstanceStatus(ctx context.Context, instanceID string, status int, result string) error
	
	// 维护功能
	CleanupOldInstances(ctx context.Context, days int) error
	RetryFailedInstance(ctx context.Context, instanceID string) error
	CancelInstance(ctx context.Context, instanceID string) error
	GetInstanceLogs(ctx context.Context, instanceID string) (string, error)
}

type collectorTaskInstanceServiceImpl struct {
	instanceDAO   dao.CollectorTaskInstanceDAO
	taskConfigDAO dao.CollectorTaskConfigDAO
	nodeDAO       cloudnodedao.SCFNodeDAO
}

// NewCollectorTaskInstanceService 创建任务实例服务
func NewCollectorTaskInstanceService(db *gorm.DB) CollectorTaskInstanceService {
	return &collectorTaskInstanceServiceImpl{
		instanceDAO:   dao.NewCollectorTaskInstanceDAO(db),
		taskConfigDAO: dao.NewCollectorTaskConfigDAO(db),
		nodeDAO:       cloudnodedao.NewSCFNodeDAO(db),
	}
}

// CreateTaskInstance 创建单个任务实例
func (s *collectorTaskInstanceServiceImpl) CreateTaskInstance(ctx context.Context, instance *model.CollectorTaskInstance) error {
	// 验证必填字段
	if instance.TaskID == "" {
		return fmt.Errorf("task ID is required")
	}
	if instance.NodeID == "" {
		return fmt.Errorf("node ID is required")
	}
	
	// 生成实例ID
	if instance.InstanceID == "" {
		instance.InstanceID = s.generateInstanceID()
	}
	
	// 验证任务配置存在
	taskConfig, err := s.taskConfigDAO.GetTaskConfig(ctx, instance.TaskID)
	if err != nil {
		return fmt.Errorf("failed to get task config: %w", err)
	}
	if taskConfig == nil {
		return fmt.Errorf("task config not found")
	}
	
	// 验证节点存在且在线
	node, err := s.nodeDAO.GetSCFNode(ctx, instance.NodeID)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}
	if node == nil {
		return fmt.Errorf("node not found")
	}
	if node.Status != model.NodeStatusOnline {
		return fmt.Errorf("node is not online")
	}
	
	// 设置默认值
	instance.Status = model.TaskInstanceStatusPending
	if instance.TargetObjects == "" {
		instance.TargetObjects = "[]"
	}
	if instance.ExecutionParams == "" {
		instance.ExecutionParams = "{}"
	}
	// 设置项目和数据集ID
	instance.ProjectID = taskConfig.ProjectID
	instance.DatasetID = taskConfig.DatasetID
	
	// 验证JSON字段
	if err := s.validateInstanceJSONFields(instance); err != nil {
		return err
	}
	
	// 合并任务配置参数到实例参数
	if err := s.mergeInstanceParams(instance, taskConfig); err != nil {
		return err
	}
	
	if err := s.instanceDAO.CreateTaskInstance(ctx, instance); err != nil {
		return fmt.Errorf("failed to create task instance: %w", err)
	}
	
	return nil
}

// BatchCreateTaskInstances 批量创建任务实例
func (s *collectorTaskInstanceServiceImpl) BatchCreateTaskInstances(ctx context.Context, taskID string, nodeAssignments map[string][]string) error {
	if taskID == "" {
		return fmt.Errorf("task ID is required")
	}
	if len(nodeAssignments) == 0 {
		return fmt.Errorf("node assignments are required")
	}
	
	// 获取任务配置
	taskConfig, err := s.taskConfigDAO.GetTaskConfig(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task config: %w", err)
	}
	if taskConfig == nil {
		return fmt.Errorf("task config not found")
	}
	
	var instances []*model.CollectorTaskInstance
	for nodeID, objects := range nodeAssignments {
		// 验证节点存在且在线
		node, err := s.nodeDAO.GetSCFNode(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("failed to get node %s: %w", nodeID, err)
		}
		if node == nil {
			return fmt.Errorf("node %s not found", nodeID)
		}
		if node.Status != model.NodeStatusOnline {
			continue // 跳过离线节点
		}
		
		objectsJSON, err := json.Marshal(objects)
		if err != nil {
			return fmt.Errorf("failed to marshal objects: %w", err)
		}
		
		instance := &model.CollectorTaskInstance{
			InstanceID:      s.generateInstanceID(),
			TaskID:          taskID,
			ProjectID:       taskConfig.ProjectID,
			DatasetID:       taskConfig.DatasetID,
			NodeID:          nodeID,
			TargetObjects:   string(objectsJSON),
			ExecutionParams: "{}",
			Status:          model.TaskInstanceStatusPending,
		}
		
		// 合并任务配置参数到实例参数
		if err := s.mergeInstanceParams(instance, taskConfig); err != nil {
			return fmt.Errorf("failed to merge params for node %s: %w", nodeID, err)
		}
		
		instances = append(instances, instance)
	}
	
	if len(instances) == 0 {
		return fmt.Errorf("no valid instances to create")
	}
	
	if err := s.instanceDAO.BatchCreateInstances(ctx, instances); err != nil {
		return fmt.Errorf("failed to batch create instances: %w", err)
	}
	
	// 更新任务配置的分发时间和结果
	result := fmt.Sprintf("Created %d instances for %d nodes", len(instances), len(nodeAssignments))
	if err := s.taskConfigDAO.UpdateDispatchResult(ctx, taskID, result); err != nil {
		// 记录错误但不影响实例创建
		fmt.Printf("failed to update dispatch result: %v\n", err)
	}
	
	return nil
}

// GetTaskInstance 获取任务实例详情
func (s *collectorTaskInstanceServiceImpl) GetTaskInstance(ctx context.Context, instanceID string) (*model.CollectorTaskInstance, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("instance ID is required")
	}
	
	instance, err := s.instanceDAO.GetTaskInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task instance: %w", err)
	}
	
	if instance == nil {
		return nil, fmt.Errorf("task instance not found")
	}
	
	return instance, nil
}

// GetTaskInstancesByNode 获取节点的任务实例
func (s *collectorTaskInstanceServiceImpl) GetTaskInstancesByNode(ctx context.Context, nodeID string, status []int) ([]*model.CollectorTaskInstance, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("node ID is required")
	}
	
	instances, err := s.instanceDAO.GetTaskInstancesByNode(ctx, nodeID, status)
	if err != nil {
		return nil, fmt.Errorf("failed to get task instances by node: %w", err)
	}
	
	return instances, nil
}

// GetTaskInstancesByTask 获取任务的实例历史
func (s *collectorTaskInstanceServiceImpl) GetTaskInstancesByTask(ctx context.Context, taskID string, limit int) ([]*model.CollectorTaskInstance, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task ID is required")
	}
	
	instances, err := s.instanceDAO.GetTaskInstancesByTask(ctx, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get task instances by task: %w", err)
	}
	
	return instances, nil
}

// GetPendingInstances 获取待执行的实例
func (s *collectorTaskInstanceServiceImpl) GetPendingInstances(ctx context.Context, nodeID string) ([]*model.CollectorTaskInstance, error) {
	instances, err := s.instanceDAO.GetPendingInstances(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending instances: %w", err)
	}
	
	return instances, nil
}

// GetRunningInstances 获取正在执行的实例
func (s *collectorTaskInstanceServiceImpl) GetRunningInstances(ctx context.Context, nodeID string) ([]*model.CollectorTaskInstance, error) {
	instances, err := s.instanceDAO.GetRunningInstances(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get running instances: %w", err)
	}
	
	return instances, nil
}

// GetRecentInstances 获取最近的任务实例
func (s *collectorTaskInstanceServiceImpl) GetRecentInstances(ctx context.Context, hours int) ([]*model.CollectorTaskInstance, error) {
	if hours <= 0 {
		hours = 24 // 默认24小时
	}
	
	instances, err := s.instanceDAO.GetRecentInstances(ctx, hours)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent instances: %w", err)
	}
	
	return instances, nil
}

// StartInstance 开始执行实例
func (s *collectorTaskInstanceServiceImpl) StartInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	
	// 验证实例状态
	instance, err := s.instanceDAO.GetTaskInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if instance == nil {
		return fmt.Errorf("instance not found")
	}
	if instance.Status != model.TaskInstanceStatusPending {
		return fmt.Errorf("instance is not in pending status")
	}
	
	if err := s.instanceDAO.StartInstance(ctx, instanceID); err != nil {
		return fmt.Errorf("failed to start instance: %w", err)
	}
	
	return nil
}

// CompleteInstance 完成实例执行
func (s *collectorTaskInstanceServiceImpl) CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	
	// 验证实例状态
	instance, err := s.instanceDAO.GetTaskInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if instance == nil {
		return fmt.Errorf("instance not found")
	}
	if instance.Status != model.TaskInstanceStatusRunning {
		return fmt.Errorf("instance is not in running status")
	}
	
	if err := s.instanceDAO.CompleteInstance(ctx, instanceID, success, result); err != nil {
		return fmt.Errorf("failed to complete instance: %w", err)
	}
	
	return nil
}

// UpdateInstanceStatus 更新实例状态
func (s *collectorTaskInstanceServiceImpl) UpdateInstanceStatus(ctx context.Context, instanceID string, status int, result string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	
	// 验证状态值
	validStatus := []int{
		model.TaskInstanceStatusPending,
		model.TaskInstanceStatusRunning,
		model.TaskInstanceStatusSuccess,
		model.TaskInstanceStatusFailed,
		model.TaskInstanceStatusTimeout,
	}
	valid := false
	for _, s := range validStatus {
		if status == s {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid status: %d", status)
	}
	
	if err := s.instanceDAO.UpdateInstanceStatus(ctx, instanceID, status, result); err != nil {
		return fmt.Errorf("failed to update instance status: %w", err)
	}
	
	return nil
}

// CleanupOldInstances 清理旧的实例记录
func (s *collectorTaskInstanceServiceImpl) CleanupOldInstances(ctx context.Context, days int) error {
	if days <= 0 {
		days = 30 // 默认保留30天
	}
	
	if err := s.instanceDAO.CleanupOldInstances(ctx, days); err != nil {
		return fmt.Errorf("failed to cleanup old instances: %w", err)
	}
	
	return nil
}

// RetryFailedInstance 重试失败的实例
func (s *collectorTaskInstanceServiceImpl) RetryFailedInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	
	// 获取原实例信息
	instance, err := s.instanceDAO.GetTaskInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if instance == nil {
		return fmt.Errorf("instance not found")
	}
	if instance.Status != model.TaskInstanceStatusFailed {
		return fmt.Errorf("instance is not in failed status")
	}
	
	// 创建新的实例作为重试
	newInstance := &model.CollectorTaskInstance{
		InstanceID:       s.generateInstanceID(),
		TaskID:           instance.TaskID,
		ProjectID:        instance.ProjectID,
		DatasetID:        instance.DatasetID,
		NodeID:           instance.NodeID,
		TargetObjects:    instance.TargetObjects,
		ExecutionParams:  instance.ExecutionParams,
		Status:           model.TaskInstanceStatusPending,
	}
	
	if err := s.instanceDAO.CreateTaskInstance(ctx, newInstance); err != nil {
		return fmt.Errorf("failed to create retry instance: %w", err)
	}
	
	return nil
}

// CancelInstance 取消正在执行的实例
func (s *collectorTaskInstanceServiceImpl) CancelInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	
	// 获取实例信息
	instance, err := s.instanceDAO.GetTaskInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if instance == nil {
		return fmt.Errorf("instance not found")
	}
	
	// 只能取消pending或running状态的任务
	if instance.Status != model.TaskInstanceStatusPending && instance.Status != model.TaskInstanceStatusRunning {
		return fmt.Errorf("instance cannot be canceled in current status")
	}
	
	// 更新状态为已取消
	return s.instanceDAO.UpdateInstanceStatus(ctx, instanceID, model.TaskInstanceStatusCanceled, "{\"message\": \"Task canceled by user\"}")
}

// GetInstanceLogs 获取实例执行日志
func (s *collectorTaskInstanceServiceImpl) GetInstanceLogs(ctx context.Context, instanceID string) (string, error) {
	if instanceID == "" {
		return "", fmt.Errorf("instance ID is required")
	}
	
	// 获取实例信息
	instance, err := s.instanceDAO.GetTaskInstance(ctx, instanceID)
	if err != nil {
		return "", fmt.Errorf("failed to get instance: %w", err)
	}
	if instance == nil {
		return "", fmt.Errorf("instance not found")
	}
	
	// TODO: 实际项目中应该从日志存储系统中获取日志
	// 这里简单返回一些模拟日志
	logs := fmt.Sprintf(`[%s] Task instance started
Node: %s
Task ID: %s
Assigned objects: %s
Status: %s
`, 
		instance.CreateTime.Format("2006-01-02 15:04:05"),
		instance.NodeID,
		instance.TaskID,
		instance.TargetObjects,
		s.getStatusString(instance.Status),
	)
	
	if instance.Result != "{}" {
		logs += fmt.Sprintf("\nResult: %s\n", instance.Result)
	}
	
	return logs, nil
}

// getStatusString 获取状态字符串
func (s *collectorTaskInstanceServiceImpl) getStatusString(status int) string {
	switch status {
	case model.TaskInstanceStatusPending:
		return "Pending"
	case model.TaskInstanceStatusRunning:
		return "Running"
	case model.TaskInstanceStatusSuccess:
		return "Success"
	case model.TaskInstanceStatusFailed:
		return "Failed"
	case model.TaskInstanceStatusCanceled:
		return "Canceled"
	default:
		return "Unknown"
	}
}

// generateInstanceID 生成实例ID
func (s *collectorTaskInstanceServiceImpl) generateInstanceID() string {
	return fmt.Sprintf("inst-%s-%d", uuid.New().String()[:8], time.Now().Unix())
}

// validateInstanceJSONFields 验证实例JSON字段
func (s *collectorTaskInstanceServiceImpl) validateInstanceJSONFields(instance *model.CollectorTaskInstance) error {
	// 验证TargetObjects
	var objects []interface{}
	if err := json.Unmarshal([]byte(instance.TargetObjects), &objects); err != nil {
		return fmt.Errorf("invalid target_objects format: %w", err)
	}
	
	// 验证ExecutionParams
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(instance.ExecutionParams), &params); err != nil {
		return fmt.Errorf("invalid execution_params format: %w", err)
	}
	
	return nil
}

// mergeInstanceParams 合并任务配置参数到实例参数
func (s *collectorTaskInstanceServiceImpl) mergeInstanceParams(instance *model.CollectorTaskInstance, taskConfig *model.CollectorTaskConfig) error {
	// 解析任务配置的采集参数
	var collectParams map[string]interface{}
	if taskConfig.CollectParams != "" {
		if err := json.Unmarshal([]byte(taskConfig.CollectParams), &collectParams); err != nil {
			return fmt.Errorf("failed to parse collect params: %w", err)
		}
	} else {
		collectParams = make(map[string]interface{})
	}
	
	// 解析任务配置的调度参数
	var scheduleConfig map[string]interface{}
	if taskConfig.ScheduleConfig != "" {
		if err := json.Unmarshal([]byte(taskConfig.ScheduleConfig), &scheduleConfig); err != nil {
			return fmt.Errorf("failed to parse schedule config: %w", err)
		}
	} else {
		scheduleConfig = make(map[string]interface{})
	}
	
	// 解析实例当前参数
	var instanceParams map[string]interface{}
	if instance.ExecutionParams != "" {
		if err := json.Unmarshal([]byte(instance.ExecutionParams), &instanceParams); err != nil {
			return fmt.Errorf("failed to parse execution params: %w", err)
		}
	} else {
		instanceParams = make(map[string]interface{})
	}
	
	// 合并参数：实例参数 > 采集参数 > 调度配置
	mergedParams := make(map[string]interface{})
	
	// 先添加调度配置
	for k, v := range scheduleConfig {
		mergedParams[k] = v
	}
	
	// 添加采集参数（覆盖调度配置）
	for k, v := range collectParams {
		mergedParams[k] = v
	}
	
	// 添加实例参数（覆盖前面的所有）
	for k, v := range instanceParams {
		mergedParams[k] = v
	}
	
	// 添加任务相关信息
	mergedParams["task_id"] = taskConfig.TaskID
	mergedParams["collector_type"] = taskConfig.CollectorType
	mergedParams["source_name"] = taskConfig.SourceName
	
	// 序列化回JSON
	mergedJSON, err := json.Marshal(mergedParams)
	if err != nil {
		return fmt.Errorf("failed to marshal merged params: %w", err)
	}
	
	instance.ExecutionParams = string(mergedJSON)
	return nil
}

// GetTaskInstanceList 获取任务实例列表
func (s *collectorTaskInstanceServiceImpl) GetTaskInstanceList(ctx context.Context, nodeID string, limit, offset int) ([]*model.CollectorTaskInstance, error) {
	if limit <= 0 {
		limit = 50 // 默认50条
	}
	if offset < 0 {
		offset = 0
	}
	
	// 如果指定了nodeID，获取该节点的实例
	if nodeID != "" {
		return s.instanceDAO.GetTaskInstancesByNode(ctx, nodeID, nil)
	}
	
	// 否则获取所有实例
	return s.instanceDAO.GetRecentInstances(ctx, 24) // 获取最近24小时的实例
}

// UpdateTaskInstance 更新任务实例
func (s *collectorTaskInstanceServiceImpl) UpdateTaskInstance(ctx context.Context, instanceID string, instance *model.CollectorTaskInstance) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	if instance == nil {
		return fmt.Errorf("instance is required")
	}
	
	// 检查实例是否存在
	existing, err := s.instanceDAO.GetTaskInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get existing instance: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("instance not found")
	}
	
	// 更新实例
	instance.InstanceID = instanceID
	if err := s.instanceDAO.UpdateTaskInstance(ctx, instance); err != nil {
		return fmt.Errorf("failed to update task instance: %w", err)
	}
	
	return nil
}

// RemoveTaskInstance 删除任务实例
func (s *collectorTaskInstanceServiceImpl) RemoveTaskInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	
	// 检查实例是否存在
	existing, err := s.instanceDAO.GetTaskInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get existing instance: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("instance not found")
	}
	
	// 检查实例状态，正在运行的实例不能删除
	if existing.Status == model.TaskInstanceStatusRunning {
		return fmt.Errorf("cannot remove running instance")
	}
	
	// 删除实例
	if err := s.instanceDAO.DeleteTaskInstance(ctx, instanceID); err != nil {
		return fmt.Errorf("failed to remove task instance: %w", err)
	}
	
	return nil
}

// StartTaskInstance 启动任务实例（与StartInstance相同）
func (s *collectorTaskInstanceServiceImpl) StartTaskInstance(ctx context.Context, instanceID string) error {
	return s.StartInstance(ctx, instanceID)
}

// StopTaskInstance 停止任务实例
func (s *collectorTaskInstanceServiceImpl) StopTaskInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	
	// 获取实例
	instance, err := s.instanceDAO.GetTaskInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if instance == nil {
		return fmt.Errorf("instance not found")
	}
	
	// 检查实例状态
	if instance.Status != model.TaskInstanceStatusRunning {
		return fmt.Errorf("instance is not running")
	}
	
	// 更新状态为已停止
	if err := s.instanceDAO.UpdateInstanceStatus(ctx, instanceID, model.TaskInstanceStatusStopped, "Task stopped by user"); err != nil {
		return fmt.Errorf("failed to stop task instance: %w", err)
	}
	
	return nil
}