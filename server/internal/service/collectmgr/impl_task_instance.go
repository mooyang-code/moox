package collectmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"

	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	collectordao "github.com/mooyang-code/moox/server/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

type TaskInstanceServiceImpl struct {
	instanceDAO     collectordao.CollectorTaskInstanceDAO
	taskRulesDAO    collectordao.CollectorTaskRulesDAO
	nodeDAO         cloudnodedao.CloudNodeDAO
	heartbeatDAO    cloudnodedao.HeartbeatDAO
	functionInvoker CloudFunctionInvoker // 使用接口而不是具体类型
}

func NewTaskInstanceServiceImpl(instanceDAO collectordao.CollectorTaskInstanceDAO,
	taskRulesDAO collectordao.CollectorTaskRulesDAO,
	nodeDAO cloudnodedao.CloudNodeDAO,
	heartbeatDAO cloudnodedao.HeartbeatDAO) TaskInstanceService {
	return &TaskInstanceServiceImpl{
		instanceDAO:  instanceDAO,
		taskRulesDAO: taskRulesDAO,
		nodeDAO:      nodeDAO,
		heartbeatDAO: heartbeatDAO,
	}
}

func (s *TaskInstanceServiceImpl) CreateTaskInstance(ctx context.Context, instance *TaskInstanceDTO) error {
	modelInstance := &model.CollectorTaskInstance{
		TaskID:          instance.TaskID,
		RuleID:          instance.RuleID,
		NodeID:          instance.NodeID,
		Symbol:          instance.Symbol,
		CollectDataType: instance.CollectDataType,
		TaskParams:      instance.TaskParams,
		Status:          instance.Status,
		LastExecTime:    instance.LastExecTime,
		Result:          instance.Result,
	}

	return s.instanceDAO.CreateTaskInstance(ctx, modelInstance)
}

func (s *TaskInstanceServiceImpl) GetTaskInstance(ctx context.Context, instanceID string) (*TaskInstanceDTO, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("instance ID is required")
	}

	instance, err := s.instanceDAO.GetTaskInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task instance: %w", err)
	}
	if instance == nil {
		return nil, fmt.Errorf("task instance not found:%s", instanceID)
	}

	return &TaskInstanceDTO{
		ID:              instance.ID,
		TaskID:          instance.TaskID,
		RuleID:          instance.RuleID,
		NodeID:          instance.NodeID,
		Symbol:          instance.Symbol,
		CollectDataType: instance.CollectDataType,
		TaskParams:      instance.TaskParams,
		Status:          instance.Status,
		LastExecTime:    instance.LastExecTime,
		Result:          instance.Result,
		Invalid:         instance.Invalid,
		CreateTime:      instance.CreateTime,
		ModifyTime:      instance.ModifyTime,
	}, nil
}

func (s *TaskInstanceServiceImpl) GetTaskInstanceList(ctx context.Context, nodeID string, limit, offset int) ([]*TaskInstanceDTO, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	if nodeID != "" {
		return s.GetTaskInstancesByNode(ctx, nodeID, nil)
	}

	return s.GetRecentInstances(ctx, 24)
}

func (s *TaskInstanceServiceImpl) UpdateTaskInstance(ctx context.Context, instanceID string, instance *TaskInstanceDTO) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	if instance == nil {
		return fmt.Errorf("instance is required")
	}

	modelInstance := &model.CollectorTaskInstance{
		ID:           instance.ID,
		TaskID:       instanceID,
		RuleID:       instance.RuleID,
		NodeID:       instance.NodeID,
		TaskParams:   instance.TaskParams,
		Status:       instance.Status,
		LastExecTime: instance.LastExecTime,
		Result:       instance.Result,
	}

	return s.instanceDAO.UpdateTaskInstance(ctx, modelInstance)
}

func (s *TaskInstanceServiceImpl) RemoveTaskInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}

	return s.instanceDAO.DeleteTaskInstance(ctx, instanceID)
}

func (s *TaskInstanceServiceImpl) StartInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}

	return s.instanceDAO.StartInstance(ctx, instanceID)
}

func (s *TaskInstanceServiceImpl) CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}

	return s.instanceDAO.CompleteInstance(ctx, instanceID, success, result)
}

// ReportTaskStatus 上报任务状态（客户端上报用）
func (s *TaskInstanceServiceImpl) ReportTaskStatus(ctx context.Context, instanceID string, status int, result string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}

	// 更新任务状态
	if err := s.instanceDAO.ReportInstanceStatus(ctx, instanceID, status, result); err != nil {
		return err
	}

	// 如果任务失败或部分失败，触发任务转移逻辑
	if status == model.InstanceStatusFailed || status == model.InstanceStatusPartFailed {
		log.InfoContextf(ctx, "[TaskInstance] Task %s failed/part-failed, triggering transfer logic", instanceID)
		// 异步执行任务转移，避免阻塞上报接口
		go s.tryTransferFailedTask(trpc.CloneContext(ctx), instanceID)
	}

	return nil
}

// InvalidateTaskInstance 作废任务实例
func (s *TaskInstanceServiceImpl) InvalidateTaskInstance(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("task ID is required")
	}

	// 调用 DAO 层批量作废方法（传入单个任务ID）
	if err := s.instanceDAO.BatchInvalidate(ctx, []string{taskID}); err != nil {
		return fmt.Errorf("failed to invalidate task instance: %w", err)
	}

	log.InfoContextf(ctx, "[TaskInstance] Task instance %s has been invalidated", taskID)
	return nil
}

// 辅助方法
func (s *TaskInstanceServiceImpl) GetTaskInstancesByNode(ctx context.Context, nodeID string, status []int) ([]*TaskInstanceDTO, error) {
	instances, err := s.instanceDAO.GetTaskInstancesByNode(ctx, nodeID, status)
	if err != nil {
		return nil, fmt.Errorf("failed to get task instances by node: %w", err)
	}

	var result []*TaskInstanceDTO
	for _, instance := range instances {
		dto := &TaskInstanceDTO{
			ID:           instance.ID,
			TaskID:       instance.TaskID,
			RuleID:       instance.RuleID,
			NodeID:       instance.NodeID,
			Symbol:       instance.Symbol,
			TaskParams:   instance.TaskParams,
			Status:       instance.Status,
			LastExecTime: instance.LastExecTime,
			Result:       instance.Result,
			Invalid:      instance.Invalid,
			CreateTime:   instance.CreateTime,
			ModifyTime:   instance.ModifyTime,
		}
		result = append(result, dto)
	}
	return result, nil
}

func (s *TaskInstanceServiceImpl) GetRecentInstances(ctx context.Context, hours int) ([]*TaskInstanceDTO, error) {
	if hours <= 0 {
		hours = 24
	}

	instances, err := s.instanceDAO.GetRecentInstances(ctx, hours)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent instances: %w", err)
	}

	var result []*TaskInstanceDTO
	for _, instance := range instances {
		dto := &TaskInstanceDTO{
			ID:           instance.ID,
			TaskID:       instance.TaskID,
			RuleID:       instance.RuleID,
			NodeID:       instance.NodeID,
			Symbol:       instance.Symbol,
			TaskParams:   instance.TaskParams,
			Status:       instance.Status,
			LastExecTime: instance.LastExecTime,
			Result:       instance.Result,
			Invalid:      instance.Invalid,
			CreateTime:   instance.CreateTime,
			ModifyTime:   instance.ModifyTime,
		}
		result = append(result, dto)
	}
	return result, nil
}

// ListTaskInstances 分页查询任务实例
func (s *TaskInstanceServiceImpl) ListTaskInstances(ctx context.Context, nodeID, ruleID string, page, size int) ([]*TaskInstanceDTO, int64, error) {
	instances, total, err := s.instanceDAO.ListInstancesWithPagination(ctx, nodeID, ruleID, page, size)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list task instances: %w", err)
	}

	var result []*TaskInstanceDTO
	for _, instance := range instances {
		dto := &TaskInstanceDTO{
			ID:           instance.ID,
			TaskID:       instance.TaskID,
			RuleID:       instance.RuleID,
			NodeID:       instance.NodeID,
			Symbol:       instance.Symbol,
			TaskParams:   instance.TaskParams,
			Status:       instance.Status,
			LastExecTime: instance.LastExecTime,
			Result:       instance.Result,
			Invalid:      instance.Invalid,
			CreateTime:   instance.CreateTime,
			ModifyTime:   instance.ModifyTime,
		}
		result = append(result, dto)
	}
	return result, total, nil
}

// ListTaskInstancesWithFilter 带筛选条件的分页查询任务实例
func (s *TaskInstanceServiceImpl) ListTaskInstancesWithFilter(ctx context.Context, filter *TaskInstanceFilterDTO) ([]*TaskInstanceDTO, int64, error) {
	// 转换DTO为DAO过滤器
	daoFilter := &collectordao.InstanceFilter{
		TaskID:   filter.TaskID,
		RuleID:   filter.RuleID,
		NodeID:   filter.NodeID,
		Symbol:   filter.Symbol,
		Status:   filter.Status,
		Invalid:  filter.Invalid,
		Page:     filter.Page,
		PageSize: filter.PageSize,
	}

	instances, total, err := s.instanceDAO.ListInstancesWithFilter(ctx, daoFilter)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list task instances with filter: %w", err)
	}

	// 收集所有唯一的 RuleID
	ruleIDSet := make(map[string]bool)
	for _, instance := range instances {
		ruleIDSet[instance.RuleID] = true
	}

	// 批量查询规则信息，获取 DataType
	ruleDataTypeMap := make(map[string]string)
	for ruleID := range ruleIDSet {
		rule, err := s.taskRulesDAO.GetTaskRule(ctx, ruleID)
		if err != nil {
			log.WarnContextf(ctx, "[ListTaskInstancesWithFilter] Failed to get rule %s: %v", ruleID, err)
			continue
		}
		if rule != nil {
			ruleDataTypeMap[ruleID] = rule.DataType
		}
	}

	var result []*TaskInstanceDTO
	for _, instance := range instances {
		dto := &TaskInstanceDTO{
			ID:           instance.ID,
			TaskID:       instance.TaskID,
			RuleID:       instance.RuleID,
			NodeID:       instance.NodeID,
			Symbol:       instance.Symbol,
			DataType:     ruleDataTypeMap[instance.RuleID], // 从规则信息中获取数据类型
			TaskParams:   instance.TaskParams,
			Status:       instance.Status,
			LastExecTime: instance.LastExecTime,
			Result:       instance.Result,
			Invalid:      instance.Invalid,
			CreateTime:   instance.CreateTime,
			ModifyTime:   instance.ModifyTime,
		}
		result = append(result, dto)
	}
	return result, total, nil
}

// SetCloudNodeService 设置CloudNodeService（解决循环依赖）
func (s *TaskInstanceServiceImpl) SetCloudNodeService(service CloudFunctionInvoker) {
	s.functionInvoker = service
}

// ========== 任务转移相关私有方法 ==========

// tryTransferFailedTask 尝试转移失败的任务到成功节点
// 注意：客户端在上报失败前已经进行了多次重试，所以这里接到失败上报就立即触发转移
// 使用负载均衡策略：从所有成功节点中随机选择一个，避免单个节点承载过多任务
func (s *TaskInstanceServiceImpl) tryTransferFailedTask(ctx context.Context, taskID string) {
	// 1. 获取任务实例信息
	instance, err := s.instanceDAO.GetTaskInstance(ctx, taskID)
	if err != nil {
		log.ErrorContextf(ctx, "[TaskTransfer] Failed to get task instance %s: %v", taskID, err)
		return
	}
	if instance == nil {
		log.WarnContextf(ctx, "[TaskTransfer] Task instance not found: %s", taskID)
		return
	}

	log.InfoContextf(ctx, "[TaskTransfer] Starting transfer for task %s (current node: %s)", taskID, instance.NodeID)

	// 2. 查找同规则下所有执行成功的节点
	successNodeIDs, err := s.instanceDAO.FindAllSuccessNodesByRule(ctx, instance.RuleID, instance.NodeID)
	if err != nil {
		log.ErrorContextf(ctx, "[TaskTransfer] Failed to find success nodes for rule %s: %v", instance.RuleID, err)
		// 记录错误信息到 result
		errorMsg := fmt.Sprintf("未找到成功节点: %v", err)
		_ = s.instanceDAO.ReportInstanceStatus(ctx, taskID, instance.Status, errorMsg)
		return
	}

	log.InfoContextf(ctx, "[TaskTransfer] Found %d success nodes for rule %s: %v",
		len(successNodeIDs), instance.RuleID, successNodeIDs)

	// 3. 从成功节点列表中随机选择一个（负载均衡）
	selectedNodeID := s.selectNodeWithLoadBalance(successNodeIDs)
	log.InfoContextf(ctx, "[TaskTransfer] Selected node %s for task %s (load balancing)", selectedNodeID, taskID)

	// 4. 更新任务实例的节点ID
	if err := s.instanceDAO.UpdateInstanceNodeID(ctx, taskID, selectedNodeID); err != nil {
		log.ErrorContextf(ctx, "[TaskTransfer] Failed to update instance node for task %s: %v", taskID, err)
		return
	}

	// 5. 立即触发任务在新节点上执行
	if err := s.triggerTaskOnNode(ctx, selectedNodeID, instance); err != nil {
		log.ErrorContextf(ctx, "[TaskTransfer] Failed to trigger task on node %s: %v", selectedNodeID, err)
		// 记录错误信息到 result
		errorMsg := fmt.Sprintf("触发失败: %v", err)
		_ = s.instanceDAO.ReportInstanceStatus(ctx, taskID, instance.Status, errorMsg)
		return
	}

	log.InfoContextf(ctx, "[TaskTransfer] Successfully transferred task %s from %s to %s",
		taskID, instance.NodeID, selectedNodeID)
}

// selectNodeWithLoadBalance 从节点列表中选择一个节点（负载均衡策略：随机选择）
// 使用随机选择策略，将失败任务打散分配到多个成功节点上，避免单点过载
func (s *TaskInstanceServiceImpl) selectNodeWithLoadBalance(nodeIDs []string) string {
	if len(nodeIDs) == 0 {
		return ""
	}

	if len(nodeIDs) == 1 {
		return nodeIDs[0]
	}

	randomIndex := rand.Intn(len(nodeIDs))
	return nodeIDs[randomIndex]
}

// triggerTaskOnNode 在指定节点上立即触发任务执行
func (s *TaskInstanceServiceImpl) triggerTaskOnNode(ctx context.Context, nodeID string, instance *model.CollectorTaskInstance) error {
	if s.functionInvoker == nil {
		return fmt.Errorf("functionInvoker not initialized")
	}

	// 1. 解析任务参数
	var taskParams map[string]interface{}
	if err := json.Unmarshal([]byte(instance.TaskParams), &taskParams); err != nil {
		return fmt.Errorf("failed to unmarshal task params: %w", err)
	}

	// 2. 构造事件数据
	eventData := map[string]interface{}{
		"action": "task", // 添加 action 字段
		"data": map[string]interface{}{
			"task_id":     instance.TaskID,
			"data_type":   taskParams["data_type"],
			"data_source": taskParams["data_source"],
			"inst_type":   taskParams["inst_type"],
			"symbol":      instance.Symbol,
			"intervals":   taskParams["intervals"],
			"immediate":   true, // 标记为立即执行
		},
	}

	eventDataJSON, err := json.Marshal(eventData)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}

	// 3. 调用云节点服务触发任务
	log.InfoContextf(ctx, "[TaskTransfer] Invoking function on node %s with event: %s", nodeID, string(eventDataJSON))
	result, err := s.functionInvoker.InvokeFunction(ctx, nodeID, eventData)
	if err != nil {
		return fmt.Errorf("failed to invoke function: %w", err)
	}

	log.InfoContextf(ctx, "[TaskTransfer] Function invocation result: %v", result)
	return nil
}
