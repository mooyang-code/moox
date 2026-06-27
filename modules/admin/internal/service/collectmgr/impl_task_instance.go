package collectmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	cloudnodedao "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/dao"
	collectordao "github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/planner"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/spacecontext"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-database/localcache"
	"trpc.group/trpc-go/trpc-go/log"
)

// TaskInstanceServiceImpl 实现采集任务实例业务服务。
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

// taskInstanceListCachePayload 保存任务实例列表缓存的载荷。
type taskInstanceListCachePayload struct {
	Items []*pb.TaskInstance
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

func (s *TaskInstanceServiceImpl) CreateTaskInstance(ctx context.Context, instance *pb.TaskInstance) error {
	modelInstance := taskInstancePBToModel(instance)
	if err := s.instanceDAO.CreateTaskInstance(ctx, modelInstance); err != nil {
		return err
	}
	s.warnIfInvalidateCacheFailed(ctx, "CreateTaskInstance", s.InvalidateTaskInstanceCache(ctx))
	return nil
}

func (s *TaskInstanceServiceImpl) GetTaskInstance(ctx context.Context, instanceID string) (*pb.TaskInstance, error) {
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
	return taskInstanceModelToPB(instance), nil
}

func (s *TaskInstanceServiceImpl) GetTaskInstanceList(ctx context.Context, nodeID string, limit, offset int) ([]*pb.TaskInstance, error) {
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

func (s *TaskInstanceServiceImpl) UpdateTaskInstance(ctx context.Context, instanceID string, instance *pb.TaskInstance) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	if instance == nil {
		return fmt.Errorf("instance is required")
	}

	modelInstance := taskInstancePBToModel(instance)
	modelInstance.TaskID = instanceID
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
func (s *TaskInstanceServiceImpl) ReportTaskStatus(ctx context.Context, instanceID string, nodeID string, status int, result string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}

	now := time.Now()
	if err := s.instanceDAO.ReportInstanceStatus(ctx, instanceID, nodeID, status, result); err != nil {
		return err
	}
	if s.memStore != nil {
		s.memStore.UpdateStatusWithNode(instanceID, nodeID, status, &now, result)
	}
	s.warnIfInvalidateCacheFailed(ctx, "ReportTaskStatus", s.InvalidateTaskInstanceCache(ctx))
	log.DebugContextf(ctx, "[TaskInstance] Status updated: taskID=%s, nodeID=%s, status=%d", instanceID, nodeID, status)
	return nil
}

// InvalidateTaskInstance 作废任务实例
func (s *TaskInstanceServiceImpl) InvalidateTaskInstance(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("task ID is required")
	}

	if err := s.instanceDAO.BatchInvalidate(ctx, []string{taskID}); err != nil {
		return fmt.Errorf("failed to invalidate task instance: %w", err)
	}
	s.warnIfInvalidateCacheFailed(ctx, "InvalidateTaskInstance", s.InvalidateTaskInstanceCache(ctx))

	log.InfoContextf(ctx, "[TaskInstance] Task instance %s has been invalidated", taskID)
	return nil
}

// 辅助方法

func (s *TaskInstanceServiceImpl) GetTaskInstancesByNode(ctx context.Context, nodeID string, status []int) ([]*pb.TaskInstance, error) {
	instances, err := s.instanceDAO.GetTaskInstancesByNode(ctx, nodeID, status)
	if err != nil {
		return nil, fmt.Errorf("failed to get task instances by node: %w", err)
	}
	result := make([]*pb.TaskInstance, 0, len(instances))
	for _, instance := range instances {
		result = append(result, taskInstanceModelToPB(instance))
	}
	return result, nil
}

func (s *TaskInstanceServiceImpl) GetRecentInstances(ctx context.Context, hours int) ([]*pb.TaskInstance, error) {
	if hours <= 0 {
		hours = 24
	}

	instances, err := s.instanceDAO.GetRecentInstances(ctx, hours)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent instances: %w", err)
	}
	result := make([]*pb.TaskInstance, 0, len(instances))
	for _, instance := range instances {
		result = append(result, taskInstanceModelToPB(instance))
	}
	return result, nil
}

// ListTaskInstances 分页查询任务实例
func (s *TaskInstanceServiceImpl) ListTaskInstances(ctx context.Context, nodeID, ruleID string, page, size int) ([]*pb.TaskInstance, int64, error) {
	instances, total, err := s.instanceDAO.ListInstancesWithPagination(ctx, nodeID, ruleID, page, size)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list task instances: %w", err)
	}
	result := make([]*pb.TaskInstance, 0, len(instances))
	for _, instance := range instances {
		result = append(result, taskInstanceModelToPB(instance))
	}
	return result, total, nil
}

// ListTaskInstancesWithFilter 带筛选条件的分页查询任务实例
func (s *TaskInstanceServiceImpl) ListTaskInstancesWithFilter(ctx context.Context, filter *pb.TaskInstanceFilter) ([]*pb.TaskInstance, int64, error) {
	// 空间硬隔离：以 ctx 注入的 space_id 为准，防止前端越权传其他空间
	spaceID, _ := spacecontext.FromContext(ctx)

	// 转换 PB filter 为 DAO 过滤器
	daoFilter := pbFilterToDAOFilter(filter, spaceID)

	instances, total, err := s.instanceDAO.ListInstancesWithFilter(ctx, daoFilter)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list task instances with filter: %w", err)
	}

	// 收集所有唯一的 RuleID，批量查询规则信息补 DataType
	ruleIDSet := make(map[string]bool)
	for _, instance := range instances {
		ruleIDSet[instance.RuleID] = true
	}
	ruleDataTypeMap := make(map[string]string)
	for ruleID := range ruleIDSet {
		// rule_id 全局唯一；instance 已按 spaceID 过滤
		rule, err := s.taskRulesDAO.GetTaskRule(ctx, "", ruleID)
		if err != nil {
			log.WarnContextf(ctx, "[ListTaskInstancesWithFilter] Failed to get rule %s: %v", ruleID, err)
			continue
		}
		if rule != nil {
			ruleDataTypeMap[ruleID] = rule.DataType
		}
	}

	result := make([]*pb.TaskInstance, 0, len(instances))
	for _, instance := range instances {
		pbInst := taskInstanceModelToPB(instance)
		pbInst.DataType = ruleDataTypeMap[instance.RuleID]
		result = append(result, pbInst)
	}
	return result, total, nil
}

// GetTaskInstanceListCache 带缓存的任务实例列表查询（仅支持精确条件、有效数据）
func (s *TaskInstanceServiceImpl) GetTaskInstanceListCache(ctx context.Context, filter *pb.TaskInstanceFilter) ([]*pb.TaskInstance, int64, error) {
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
func (s *TaskInstanceServiceImpl) tryTransferFailedTask(ctx context.Context, taskID string) {
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

	rule, err := s.taskRulesDAO.GetTaskRule(ctx, instance.SpaceID, instance.RuleID)
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
	matchingNodes, err := basePlanner.GetMatchingNodes(ctx, rule, dataType)
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

	selectedNodeID := s.selectNodeWithLoadBalance(nodeIDs)
	log.InfoContextf(ctx, "[TaskTransfer] Selected node %s for task %s", selectedNodeID, taskID)

	if err := s.instanceDAO.UpdateInstanceNodeID(ctx, taskID, selectedNodeID); err != nil {
		log.ErrorContextf(ctx, "[TaskTransfer] Failed to update instance node for task %s: %v", taskID, err)
		return
	}
	s.warnIfInvalidateCacheFailed(ctx, "tryTransferFailedTask", s.InvalidateTaskInstanceCache(ctx))

	if err := s.triggerTaskOnNode(ctx, selectedNodeID, instance); err != nil {
		log.ErrorContextf(ctx, "[TaskTransfer] Failed to trigger task on node %s: %v", selectedNodeID, err)
		errorMsg := fmt.Sprintf("触发失败: %v", err)
		_ = s.instanceDAO.ReportInstanceStatus(ctx, taskID, instance.LastExecNode, instance.LastExecStatus, errorMsg)
		return
	}

	log.InfoContextf(ctx, "[TaskTransfer] Successfully transferred task %s from %s to %s",
		taskID, instance.PlannedExecNode, selectedNodeID)
}

func (s *TaskInstanceServiceImpl) selectNodeWithLoadBalance(nodeIDs []string) string {
	if len(nodeIDs) == 0 {
		return ""
	}
	if len(nodeIDs) == 1 {
		return nodeIDs[0]
	}
	return nodeIDs[rand.Intn(len(nodeIDs))]
}

func (s *TaskInstanceServiceImpl) triggerTaskOnNode(ctx context.Context, nodeID string, instance *model.CollectorTaskInstance) error {
	if s.functionInvoker == nil {
		return fmt.Errorf("functionInvoker not initialized")
	}

	var taskParams map[string]interface{}
	if err := json.Unmarshal([]byte(instance.TaskParams), &taskParams); err != nil {
		return fmt.Errorf("failed to unmarshal task params: %w", err)
	}

	eventData := map[string]interface{}{
		"action": "task",
		"data": map[string]interface{}{
			"task_id":     instance.TaskID,
			"data_type":   taskParams["data_type"],
			"data_source": taskParams["data_source"],
			"inst_type":   taskParams["inst_type"],
			"symbol":      instance.Symbol,
			"intervals":   taskParams["intervals"],
			"immediate":   true,
		},
	}

	eventDataJSON, err := json.Marshal(eventData)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}

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

// pbFilterToDAOFilter 将 pb.TaskInstanceFilter 转为 dao.InstanceFilter。
// spaceID 由 ctx 注入，不从请求体取。
func pbFilterToDAOFilter(f *pb.TaskInstanceFilter, spaceID string) *collectordao.InstanceFilter {
	out := &collectordao.InstanceFilter{
		SpaceID:         spaceID,
		BizType:         f.GetBizType(),
		TaskID:          f.GetTaskId(),
		RuleID:          f.GetRuleId(),
		PlannedExecNode: f.GetPlannedExecNode(),
		LastExecNode:    f.GetLastExecNode(),
		Symbol:          f.GetSymbol(),
		Page:            int(f.GetPage()),
		PageSize:        int(f.GetPageSize()),
	}
	if f.LastExecStatus != nil {
		v := int(*f.LastExecStatus)
		out.LastExecStatus = &v
	}
	if f.Invalid != nil {
		v := int(*f.Invalid)
		out.Invalid = &v
	}
	return out
}

// daoFilterToPBFilter 将 dao.InstanceFilter 转回 pb.TaskInstanceFilter（用于缓存 key 归一化）。
func daoFilterToPBFilter(f *collectordao.InstanceFilter) *pb.TaskInstanceFilter {
	out := &pb.TaskInstanceFilter{
		BizType:         f.BizType,
		TaskId:          f.TaskID,
		RuleId:          f.RuleID,
		PlannedExecNode: f.PlannedExecNode,
		LastExecNode:    f.LastExecNode,
		Symbol:          f.Symbol,
		Page:            int32(f.Page),
		PageSize:        int32(f.PageSize),
	}
	if f.LastExecStatus != nil {
		v := int32(*f.LastExecStatus)
		out.LastExecStatus = &v
	}
	if f.Invalid != nil {
		v := int32(*f.Invalid)
		out.Invalid = &v
	}
	return out
}

func buildTaskInstanceCacheKey(version string, filter *pb.TaskInstanceFilter) string {
	statusKey := "all"
	if filter.LastExecStatus != nil {
		statusKey = strconv.Itoa(int(*filter.LastExecStatus))
	}
	return fmt.Sprintf("collectmgr:task_instance:cache:list:%s:page:%d:size:%d:status:%s",
		version,
		filter.GetPage(),
		filter.GetPageSize(),
		statusKey,
	)
}

// normalizeTaskInstanceCacheFilter 归一化缓存查询 filter（仅支持精确条件、有效数据）。
func normalizeTaskInstanceCacheFilter(filter *pb.TaskInstanceFilter) (*pb.TaskInstanceFilter, error) {
	if filter == nil {
		filter = &pb.TaskInstanceFilter{}
	}
	if filter.GetTaskId() != "" || filter.GetRuleId() != "" || filter.GetPlannedExecNode() != "" || filter.GetLastExecNode() != "" || filter.GetSymbol() != "" {
		return nil, fmt.Errorf("cache mode does not support fuzzy query")
	}
	if filter.Invalid != nil && *filter.Invalid != int32(model.InvalidNo) {
		return nil, fmt.Errorf("cache mode only supports valid data")
	}

	page := int(filter.GetPage())
	if page < 1 {
		page = 1
	}
	pageSize := int(filter.GetPageSize())
	if pageSize <= 0 {
		pageSize = 10
	}
	if pageSize > 100 {
		pageSize = 100
	}

	valid := int32(model.InvalidNo)
	normalized := &pb.TaskInstanceFilter{
		LastExecStatus: filter.LastExecStatus,
		Invalid:        &valid,
		Page:           int32(page),
		PageSize:       int32(pageSize),
	}
	return normalized, nil
}

func (s *TaskInstanceServiceImpl) warnIfInvalidateCacheFailed(ctx context.Context, action string, err error) {
	if err == nil {
		return
	}
	log.WarnContextf(ctx, "[TaskInstance] %s cache invalidate failed: %v", action, err)
}

// 保留 daoFilterToPBFilter 引用（当前未对外使用，留作缓存场景扩展）。
var _ = daoFilterToPBFilter
