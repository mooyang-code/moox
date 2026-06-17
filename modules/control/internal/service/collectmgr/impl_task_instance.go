package collectmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	cloudnodedao "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/dao"
	collectordao "github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/model"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/planner"
	"trpc.group/trpc-go/trpc-database/localcache"
	"trpc.group/trpc-go/trpc-go/log"
)

type TaskInstanceServiceImpl struct {
	instanceDAO     collectordao.CollectorTaskInstanceDAO
	taskRulesDAO    collectordao.CollectorTaskRulesDAO
	nodeDAO         cloudnodedao.CloudNodeDAO
	functionInvoker CloudFunctionInvoker // 使用接口而不是具体类型
	memStore        TaskInstanceStore    // 内存仓库（状态同步）
}

const (
	taskInstanceCacheTTLSeconds        int64 = 30
	taskInstanceCacheVersionTTLSeconds int64 = 3600
	taskInstanceCacheVersionKey              = "collectmgr:task_instance:cache:version"
)

type taskInstanceListCachePayload struct {
	Items []*TaskInstanceDTO
	Total int64
}

func NewTaskInstanceServiceImpl(instanceDAO collectordao.CollectorTaskInstanceDAO,
	taskRulesDAO collectordao.CollectorTaskRulesDAO,
	nodeDAO cloudnodedao.CloudNodeDAO) TaskInstanceService {
	return &TaskInstanceServiceImpl{
		instanceDAO:  instanceDAO,
		taskRulesDAO: taskRulesDAO,
		nodeDAO:      nodeDAO,
	}
}

func (s *TaskInstanceServiceImpl) CreateTaskInstance(ctx context.Context, instance *TaskInstanceDTO) error {
	modelInstance := &model.CollectorTaskInstance{
		TaskID:          instance.TaskID,
		RuleID:          instance.RuleID,
		BizType:         instance.BizType,
		PlannedExecNode: instance.PlannedExecNode,
		LastExecNode:    instance.LastExecNode,
		LastExecStatus:  instance.LastExecStatus,
		Symbol:          instance.Symbol,
		CollectDataType: instance.CollectDataType,
		TaskParams:      instance.TaskParams,
		LastExecTime:    instance.LastExecTime,
		Result:          instance.Result,
	}

	if err := s.instanceDAO.CreateTaskInstance(ctx, modelInstance); err != nil {
		return err
	}
	s.warnIfInvalidateCacheFailed(ctx, "CreateTaskInstance", s.InvalidateTaskInstanceCache(ctx))
	return nil
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
		BizType:         instance.BizType,
		PlannedExecNode: instance.PlannedExecNode,
		LastExecNode:    instance.LastExecNode,
		LastExecStatus:  instance.LastExecStatus,
		Symbol:          instance.Symbol,
		CollectDataType: instance.CollectDataType,
		TaskParams:      instance.TaskParams,
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
		ID:              instance.ID,
		TaskID:          instanceID,
		RuleID:          instance.RuleID,
		PlannedExecNode: instance.PlannedExecNode,
		LastExecNode:    instance.LastExecNode,
		LastExecStatus:  instance.LastExecStatus,
		TaskParams:      instance.TaskParams,
		LastExecTime:    instance.LastExecTime,
		Result:          instance.Result,
	}

	if err := s.instanceDAO.UpdateTaskInstance(ctx, modelInstance); err != nil {
		return err
	}
	s.warnIfInvalidateCacheFailed(ctx, "UpdateTaskInstance", s.InvalidateTaskInstanceCache(ctx))
	return nil
}

func (s *TaskInstanceServiceImpl) RemoveTaskInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}

	if err := s.instanceDAO.DeleteTaskInstance(ctx, instanceID); err != nil {
		return err
	}
	s.warnIfInvalidateCacheFailed(ctx, "RemoveTaskInstance", s.InvalidateTaskInstanceCache(ctx))
	return nil
}

func (s *TaskInstanceServiceImpl) StartInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}

	if err := s.instanceDAO.StartInstance(ctx, instanceID); err != nil {
		return err
	}
	s.warnIfInvalidateCacheFailed(ctx, "StartInstance", s.InvalidateTaskInstanceCache(ctx))
	return nil
}

func (s *TaskInstanceServiceImpl) CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}

	if err := s.instanceDAO.CompleteInstance(ctx, instanceID, success, result); err != nil {
		return err
	}
	s.warnIfInvalidateCacheFailed(ctx, "CompleteInstance", s.InvalidateTaskInstanceCache(ctx))
	return nil
}

// ReportTaskStatus 上报任务状态（客户端上报用）
// 新增 nodeID 参数，只更新内存仓库，DB 通过 SnapshotWorker 异步刷入
func (s *TaskInstanceServiceImpl) ReportTaskStatus(ctx context.Context, instanceID string, nodeID string, status int, result string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}

	// 更新内存仓库（内存是单一事实来源）
	now := time.Now()
	s.memStore.UpdateStatusWithNode(instanceID, nodeID, status, &now, result)
	log.DebugContextf(ctx, "[TaskInstance] Status updated: taskID=%s, nodeID=%s, status=%d", instanceID, nodeID, status)

	// 如果任务失败，触发任务转移逻辑 （避免持续失败，持续转移，滚雪球，这里不进行任务转移了。 下个执行周期会重新分配任务）
	// if status == model.InstanceStatusFailed {
	// 	log.InfoContextf(ctx, "[TaskInstance] Task %s failed, triggering transfer logic", instanceID)
	// 	go s.tryTransferFailedTask(trpc.CloneContext(ctx), instanceID)
	// }

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
	s.warnIfInvalidateCacheFailed(ctx, "InvalidateTaskInstance", s.InvalidateTaskInstanceCache(ctx))

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
			ID:              instance.ID,
			TaskID:          instance.TaskID,
			RuleID:          instance.RuleID,
			PlannedExecNode: instance.PlannedExecNode,
			LastExecNode:    instance.LastExecNode,
			LastExecStatus:  instance.LastExecStatus,
			Symbol:          instance.Symbol,
			TaskParams:      instance.TaskParams,
			LastExecTime:    instance.LastExecTime,
			Result:          instance.Result,
			Invalid:         instance.Invalid,
			CreateTime:      instance.CreateTime,
			ModifyTime:      instance.ModifyTime,
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
			ID:              instance.ID,
			TaskID:          instance.TaskID,
			RuleID:          instance.RuleID,
			PlannedExecNode: instance.PlannedExecNode,
			LastExecNode:    instance.LastExecNode,
			LastExecStatus:  instance.LastExecStatus,
			Symbol:          instance.Symbol,
			TaskParams:      instance.TaskParams,
			LastExecTime:    instance.LastExecTime,
			Result:          instance.Result,
			Invalid:         instance.Invalid,
			CreateTime:      instance.CreateTime,
			ModifyTime:      instance.ModifyTime,
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
			ID:              instance.ID,
			TaskID:          instance.TaskID,
			RuleID:          instance.RuleID,
			PlannedExecNode: instance.PlannedExecNode,
			LastExecNode:    instance.LastExecNode,
			LastExecStatus:  instance.LastExecStatus,
			Symbol:          instance.Symbol,
			TaskParams:      instance.TaskParams,
			LastExecTime:    instance.LastExecTime,
			Result:          instance.Result,
			Invalid:         instance.Invalid,
			CreateTime:      instance.CreateTime,
			ModifyTime:      instance.ModifyTime,
		}
		result = append(result, dto)
	}
	return result, total, nil
}

// ListTaskInstancesWithFilter 带筛选条件的分页查询任务实例
func (s *TaskInstanceServiceImpl) ListTaskInstancesWithFilter(ctx context.Context, filter *TaskInstanceFilterDTO) ([]*TaskInstanceDTO, int64, error) {
	// 转换DTO为DAO过滤器
	daoFilter := &collectordao.InstanceFilter{
		BizType:         filter.BizType,
		TaskID:          filter.TaskID,
		RuleID:          filter.RuleID,
		PlannedExecNode: filter.PlannedExecNode,
		LastExecNode:    filter.LastExecNode,
		LastExecStatus:  filter.LastExecStatus,
		Symbol:          filter.Symbol,
		Invalid:         filter.Invalid,
		Page:            filter.Page,
		PageSize:        filter.PageSize,
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
			ID:              instance.ID,
			TaskID:          instance.TaskID,
			RuleID:          instance.RuleID,
			BizType:         instance.BizType,
			PlannedExecNode: instance.PlannedExecNode,
			LastExecNode:    instance.LastExecNode,
			LastExecStatus:  instance.LastExecStatus,
			Symbol:          instance.Symbol,
			DataType:        ruleDataTypeMap[instance.RuleID], // 从规则信息中获取数据类型
			TaskParams:      instance.TaskParams,
			LastExecTime:    instance.LastExecTime,
			Result:          instance.Result,
			Invalid:         instance.Invalid,
			CreateTime:      instance.CreateTime,
			ModifyTime:      instance.ModifyTime,
		}
		result = append(result, dto)
	}
	return result, total, nil
}

// GetTaskInstanceListCache 带缓存的任务实例列表查询（仅支持精确条件、有效数据）
func (s *TaskInstanceServiceImpl) GetTaskInstanceListCache(ctx context.Context, filter *TaskInstanceFilterDTO) ([]*TaskInstanceDTO, int64, error) {
	normalized, err := normalizeTaskInstanceCacheFilter(filter)
	if err != nil {
		return nil, 0, err
	}

	cacheKey := buildTaskInstanceCacheKey(s.getTaskInstanceCacheVersion(ctx), normalized)
	cached, err := localcache.GetWithLoad(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, error) {
		items, total, err := s.ListTaskInstancesWithFilter(ctx, normalized)
		if err != nil {
			return nil, err
		}
		return &taskInstanceListCachePayload{Items: items, Total: total}, nil
	}, taskInstanceCacheTTLSeconds)
	if err != nil {
		return nil, 0, err
	}

	payload, ok := cached.(*taskInstanceListCachePayload)
	if !ok {
		localcache.Del(cacheKey)
		return nil, 0, fmt.Errorf("invalid task instance cache payload")
	}
	return payload.Items, payload.Total, nil
}

// InvalidateTaskInstanceCache 失效任务实例缓存
func (s *TaskInstanceServiceImpl) InvalidateTaskInstanceCache(ctx context.Context) error {
	version := strconv.FormatInt(time.Now().UnixNano(), 10)
	if ok := localcache.Set(taskInstanceCacheVersionKey, version, taskInstanceCacheVersionTTLSeconds); !ok {
		return fmt.Errorf("failed to update task instance cache version")
	}
	return nil
}

// SetCloudNodeService 设置CloudNodeService（解决循环依赖）
func (s *TaskInstanceServiceImpl) SetCloudNodeService(service CloudFunctionInvoker) {
	s.functionInvoker = service
}

// SetTaskInstanceStore 设置内存仓库（状态同步）
func (s *TaskInstanceServiceImpl) SetTaskInstanceStore(store TaskInstanceStore) {
	s.memStore = store
}

// ========== 任务转移相关私有方法 ==========

// tryTransferFailedTask 尝试转移失败的任务到可执行节点
// 注意：客户端在上报失败前已经进行了多次重试，所以这里接到失败上报就立即触发转移
// 使用负载均衡策略：从匹配规则的节点中随机选择一个
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

	log.InfoContextf(ctx, "[TaskTransfer] Starting transfer for task %s (current node: %s)", taskID, instance.PlannedExecNode)

	// 2. 获取规则并匹配可执行节点
	rule, err := s.taskRulesDAO.GetTaskRule(ctx, instance.RuleID)
	if err != nil {
		log.ErrorContextf(ctx, "[TaskTransfer] Failed to get rule %s: %v", instance.RuleID, err)
		errorMsg := fmt.Sprintf("获取规则失败: %v", err)
		_ = s.instanceDAO.ReportInstanceStatus(ctx, taskID, instance.LastExecNode, instance.LastExecStatus, errorMsg)
		return
	}
	if rule == nil {
		log.WarnContextf(ctx, "[TaskTransfer] Rule not found: %s", instance.RuleID)
		_ = s.instanceDAO.ReportInstanceStatus(ctx, taskID, instance.LastExecNode, instance.LastExecStatus, "规则不存在")
		return
	}

	ruleDTO := &dto.TaskRuleDTO{
		ID:             rule.ID,
		RuleID:         rule.RuleID,
		BizType:        rule.BizType,
		DataType:       rule.DataType,
		DataSource:     rule.DataSource,
		CollectParams:  rule.CollectParams,
		AssignmentType: rule.AssignmentType,
		AssignedNodes:  rule.AssignedNodes,
		NodePattern:    rule.NodePattern,
		NodeTags:       rule.NodeTags,
		Enabled:        rule.Enabled,
		Creator:        rule.Creator,
		CreateTime:     rule.CreateTime,
		ModifyTime:     rule.ModifyTime,
	}

	dataType := rule.DataType
	if dataType == "" {
		dataType = instance.CollectDataType
	}
	if dataType == "" {
		log.WarnContextf(ctx, "[TaskTransfer] Missing data type for task %s", taskID)
		_ = s.instanceDAO.ReportInstanceStatus(ctx, taskID, instance.LastExecNode, instance.LastExecStatus, "任务数据类型为空")
		return
	}
	basePlanner := planner.NewBasePlanner(s.nodeDAO, nil, nil)
	matchingNodes, err := basePlanner.GetMatchingNodes(ctx, ruleDTO, dataType)
	if err != nil {
		log.ErrorContextf(ctx, "[TaskTransfer] Failed to get matching nodes for rule %s: %v", instance.RuleID, err)
		errorMsg := fmt.Sprintf("匹配节点失败: %v", err)
		_ = s.instanceDAO.ReportInstanceStatus(ctx, taskID, instance.LastExecNode, instance.LastExecStatus, errorMsg)
		return
	}
	if len(matchingNodes) == 0 {
		log.WarnContextf(ctx, "[TaskTransfer] No matching nodes for rule %s", instance.RuleID)
		_ = s.instanceDAO.ReportInstanceStatus(ctx, taskID, instance.LastExecNode, instance.LastExecStatus, "未找到可执行节点")
		return
	}

	nodeIDs := make([]string, 0, len(matchingNodes))
	for _, node := range matchingNodes {
		if node.NodeID != "" {
			nodeIDs = append(nodeIDs, node.NodeID)
		}
	}
	if len(nodeIDs) == 0 {
		log.WarnContextf(ctx, "[TaskTransfer] No valid node IDs for rule %s", instance.RuleID)
		_ = s.instanceDAO.ReportInstanceStatus(ctx, taskID, instance.LastExecNode, instance.LastExecStatus, "节点ID为空")
		return
	}

	if instance.PlannedExecNode != "" && len(nodeIDs) > 1 {
		filtered := nodeIDs[:0]
		for _, nodeID := range nodeIDs {
			if nodeID != instance.PlannedExecNode {
				filtered = append(filtered, nodeID)
			}
		}
		if len(filtered) > 0 {
			nodeIDs = filtered
		}
	}

	// 3. 从匹配节点列表中随机选择一个
	selectedNodeID := s.selectNodeWithLoadBalance(nodeIDs)
	log.InfoContextf(ctx, "[TaskTransfer] Selected node %s for task %s", selectedNodeID, taskID)

	// 4. 更新任务实例的节点ID
	if err := s.instanceDAO.UpdateInstanceNodeID(ctx, taskID, selectedNodeID); err != nil {
		log.ErrorContextf(ctx, "[TaskTransfer] Failed to update instance node for task %s: %v", taskID, err)
		return
	}
	s.warnIfInvalidateCacheFailed(ctx, "tryTransferFailedTask", s.InvalidateTaskInstanceCache(ctx))

	// 5. 立即触发任务在新节点上执行
	if err := s.triggerTaskOnNode(ctx, selectedNodeID, instance); err != nil {
		log.ErrorContextf(ctx, "[TaskTransfer] Failed to trigger task on node %s: %v", selectedNodeID, err)
		// 记录错误信息到 result
		errorMsg := fmt.Sprintf("触发失败: %v", err)
		_ = s.instanceDAO.ReportInstanceStatus(ctx, taskID, instance.LastExecNode, instance.LastExecStatus, errorMsg)
		return
	}

	log.InfoContextf(ctx, "[TaskTransfer] Successfully transferred task %s from %s to %s",
		taskID, instance.PlannedExecNode, selectedNodeID)
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

func (s *TaskInstanceServiceImpl) getTaskInstanceCacheVersion(ctx context.Context) string {
	if cached, ok := localcache.Get(taskInstanceCacheVersionKey); ok {
		if version, ok := cached.(string); ok && version != "" {
			return version
		}
		localcache.Del(taskInstanceCacheVersionKey)
	}
	version := strconv.FormatInt(time.Now().UnixNano(), 10)
	localcache.Set(taskInstanceCacheVersionKey, version, taskInstanceCacheVersionTTLSeconds)
	return version
}

func buildTaskInstanceCacheKey(version string, filter *TaskInstanceFilterDTO) string {
	statusKey := "all"
	if filter.LastExecStatus != nil {
		statusKey = strconv.Itoa(*filter.LastExecStatus)
	}
	return fmt.Sprintf("collectmgr:task_instance:cache:list:%s:page:%d:size:%d:status:%s",
		version,
		filter.Page,
		filter.PageSize,
		statusKey,
	)
}

func normalizeTaskInstanceCacheFilter(filter *TaskInstanceFilterDTO) (*TaskInstanceFilterDTO, error) {
	if filter == nil {
		filter = &TaskInstanceFilterDTO{}
	}
	if filter.TaskID != "" || filter.RuleID != "" || filter.PlannedExecNode != "" || filter.LastExecNode != "" || filter.Symbol != "" {
		return nil, fmt.Errorf("cache mode does not support fuzzy query")
	}
	if filter.Invalid != nil && *filter.Invalid != model.InvalidNo {
		return nil, fmt.Errorf("cache mode only supports valid data")
	}

	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 10
	}
	if pageSize > 100 {
		pageSize = 100
	}

	valid := model.InvalidNo
	return &TaskInstanceFilterDTO{
		LastExecStatus: filter.LastExecStatus,
		Invalid:        &valid,
		Page:           page,
		PageSize:       pageSize,
	}, nil
}

func (s *TaskInstanceServiceImpl) warnIfInvalidateCacheFailed(ctx context.Context, action string, err error) {
	if err == nil {
		return
	}
	log.WarnContextf(ctx, "[TaskInstance] %s cache invalidate failed: %v", action, err)
}
