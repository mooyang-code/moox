package collectmgr

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/distributor"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"

	"github.com/bwmarrin/snowflake"
	"trpc.group/trpc-go/trpc-go/log"
)

// TaskPlannerServiceImpl 任务规划器服务实现
type TaskPlannerServiceImpl struct {
	taskRulesDAO dao.CollectorTaskRulesDAO
	instanceDAO  dao.CollectorTaskInstanceDAO
	registry     *distributor.DistributorRegistry
	snowflake    *snowflake.Node
}

// NewTaskPlannerServiceImpl 创建任务规划器服务
func NewTaskPlannerServiceImpl(
	taskRulesDAO dao.CollectorTaskRulesDAO,
	instanceDAO dao.CollectorTaskInstanceDAO,
	registry *distributor.DistributorRegistry,
) TaskPlannerService {
	// 创建 snowflake 节点
	node, err := snowflake.NewNode(time.Now().Unix() % 1024)
	if err != nil {
		node, _ = snowflake.NewNode(1)
	}

	return &TaskPlannerServiceImpl{
		taskRulesDAO: taskRulesDAO,
		instanceDAO:  instanceDAO,
		registry:     registry,
		snowflake:    node,
	}
}

// SyncRuleInstances 同步指定规则的任务实例（幂等操作）
func (s *TaskPlannerServiceImpl) SyncRuleInstances(ctx context.Context, ruleID string) (*SyncResult, error) {
	log.InfoContextf(ctx, "[TaskPlanner] Starting sync for rule: %s", ruleID)

	result := &SyncResult{RuleID: ruleID}

	// 1. 获取规则详情
	ruleModel, err := s.taskRulesDAO.GetTaskRule(ctx, ruleID)
	if err != nil {
		return nil, fmt.Errorf("failed to get rule: %w", err)
	}
	if ruleModel == nil {
		return nil, fmt.Errorf("rule not found: %s", ruleID)
	}

	// 转换为 DTO
	rule := &dto.TaskRuleDTO{
		ID:             ruleModel.ID,
		RuleID:         ruleModel.RuleID,
		DataType:       ruleModel.DataType,
		DataSource:     ruleModel.DataSource,
		CollectParams:  ruleModel.CollectParams,
		AssignmentType: ruleModel.AssignmentType,
		AssignedNodes:  ruleModel.AssignedNodes,
		NodePattern:    ruleModel.NodePattern,
		Enabled:        ruleModel.Enabled,
		Creator:        ruleModel.Creator,
		CreateTime:     ruleModel.CreateTime,
		ModifyTime:     ruleModel.ModifyTime,
	}

	// 检查规则是否启用
	if rule.Enabled != model.EnabledTrue {
		log.InfoContextf(ctx, "[TaskPlanner] Rule %s is not enabled, skipping", ruleID)
		return result, nil
	}

	// 2. 获取分配器
	dist := s.registry.Get(rule.DataType)
	if dist == nil {
		dist = s.registry.GetOrDefault(rule.DataType)
		if dist == nil {
			return nil, fmt.Errorf("distributor not found for data type: %s", rule.DataType)
		}
	}

	// 3. 计算应有的实例列表
	computedInstances, err := s.computeInstances(ctx, rule, dist)
	if err != nil {
		return nil, fmt.Errorf("failed to compute instances: %w", err)
	}

	// 4. 获取现有有效实例
	existingInstances, err := s.instanceDAO.GetActiveInstancesByRule(ctx, ruleID)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing instances: %w", err)
	}

	// 5. 执行增量更新
	syncResult, err := s.diffAndSync(ctx, ruleID, existingInstances, computedInstances)
	if err != nil {
		return nil, fmt.Errorf("failed to sync instances: %w", err)
	}

	log.InfoContextf(ctx, "[TaskPlanner] Sync completed for rule %s: created=%d, updated=%d, deleted=%d, unchanged=%d",
		ruleID, syncResult.Created, syncResult.Updated, syncResult.Deleted, syncResult.Unchanged)
	return syncResult, nil
}

// computeInstances 计算应有的实例列表
func (s *TaskPlannerServiceImpl) computeInstances(ctx context.Context, rule *dto.TaskRuleDTO, dist distributor.TaskDistributor) ([]*model.CollectorTaskInstance, error) {
	var instances []*model.CollectorTaskInstance

	// 获取匹配的节点
	nodes, err := dist.GetMatchingNodes(ctx, rule)
	if err != nil {
		return nil, fmt.Errorf("failed to get matching nodes: %w", err)
	}
	if len(nodes) == 0 {
		log.InfoContextf(ctx, "[TaskPlanner] No matching nodes for rule %s", rule.RuleID)
		return instances, nil
	}

	// 获取目标对象
	objects, err := dist.GetTargetObjects(ctx, rule)
	if err != nil {
		return nil, fmt.Errorf("failed to get target objects: %w", err)
	}

	// 如果没有对象，生成一个 symbol="" 的实例（统一处理）
	if len(objects) == 0 {
		objects = []string{""}
	}

	// 为每个 node × object 组合生成实例
	for _, node := range nodes {
		for _, object := range objects {
			// 构建任务参数
			params, err := dist.BuildTaskParams(ctx, rule, object)
			if err != nil {
				log.WarnContextf(ctx, "[TaskPlanner] Failed to build task params for %s/%s: %v", node.NodeID, object, err)
				continue
			}

			instance := &model.CollectorTaskInstance{
				RuleID:     rule.RuleID,
				NodeID:     node.NodeID,
				Symbol:     object,
				TaskParams: params,
				Status:     model.InstanceStatusPending,
				Invalid:    model.InvalidNo,
			}
			instances = append(instances, instance)
		}
	}

	return instances, nil
}

// diffAndSync 差异对比与同步
func (s *TaskPlannerServiceImpl) diffAndSync(ctx context.Context, ruleID string, existing []*model.CollectorTaskInstance, computed []*model.CollectorTaskInstance) (*SyncResult, error) {
	result := &SyncResult{RuleID: ruleID}

	// 构建索引 Map，Key = RuleID + NodeID + Symbol
	existingMap := make(map[string]*model.CollectorTaskInstance)
	for _, inst := range existing {
		key := s.makeInstanceKey(inst.RuleID, inst.NodeID, inst.Symbol)
		existingMap[key] = inst
	}

	computedMap := make(map[string]*model.CollectorTaskInstance)
	for _, inst := range computed {
		key := s.makeInstanceKey(inst.RuleID, inst.NodeID, inst.Symbol)
		computedMap[key] = inst
	}

	var toCreate []*model.CollectorTaskInstance
	var toUpdate []*dao.InstanceParamUpdate
	var toInvalidate []string

	// 处理新增和更新
	for key, computedInst := range computedMap {
		if existingInst, exists := existingMap[key]; exists {
			// 已存在，检查参数是否变化
			if existingInst.TaskParams != computedInst.TaskParams {
				toUpdate = append(toUpdate, &dao.InstanceParamUpdate{
					TaskID:     existingInst.TaskID,
					TaskParams: computedInst.TaskParams,
				})
				result.Updated++
			} else {
				result.Unchanged++
			}
			delete(existingMap, key)
		} else {
			// 不存在，需要新建
			computedInst.TaskID = s.generateTaskID()
			toCreate = append(toCreate, computedInst)
			result.Created++
		}
	}

	// 处理删除（existingMap 中剩余的）
	for _, existingInst := range existingMap {
		toInvalidate = append(toInvalidate, existingInst.TaskID)
		result.Deleted++
	}

	// 批量执行数据库操作
	if len(toCreate) > 0 {
		if err := s.instanceDAO.BatchCreateInstances(ctx, toCreate); err != nil {
			return result, fmt.Errorf("failed to create instances: %w", err)
		}
	}

	if len(toUpdate) > 0 {
		if err := s.instanceDAO.BatchUpdateParams(ctx, toUpdate); err != nil {
			return result, fmt.Errorf("failed to update instances: %w", err)
		}
	}

	if len(toInvalidate) > 0 {
		if err := s.instanceDAO.BatchInvalidate(ctx, toInvalidate); err != nil {
			return result, fmt.Errorf("failed to invalidate instances: %w", err)
		}
	}

	return result, nil
}

// InvalidateRuleInstances 使规则的所有实例失效
func (s *TaskPlannerServiceImpl) InvalidateRuleInstances(ctx context.Context, ruleID string) error {
	log.InfoContextf(ctx, "[TaskPlanner] Invalidating all instances for rule: %s", ruleID)

	if err := s.instanceDAO.InvalidateInstancesByRule(ctx, ruleID); err != nil {
		return fmt.Errorf("failed to invalidate instances: %w", err)
	}

	log.InfoContextf(ctx, "[TaskPlanner] All instances invalidated for rule: %s", ruleID)
	return nil
}

// SyncAllEnabledRules 同步所有启用的规则
func (s *TaskPlannerServiceImpl) SyncAllEnabledRules(ctx context.Context) (*BatchSyncResult, error) {
	log.InfoContext(ctx, "[TaskPlanner] Starting sync for all enabled rules")

	result := &BatchSyncResult{}

	// 获取所有启用的规则
	rules, err := s.taskRulesDAO.GetTaskRulesList(ctx, "", "", model.EnabledTrue)
	if err != nil {
		return nil, fmt.Errorf("failed to get enabled rules: %w", err)
	}

	result.TotalRules = len(rules)

	// 同步每个规则
	for _, rule := range rules {
		syncResult, err := s.SyncRuleInstances(ctx, rule.RuleID)
		if err != nil {
			log.ErrorContextf(ctx, "[TaskPlanner] Failed to sync rule %s: %v", rule.RuleID, err)
			result.FailedRules++
			result.Errors = append(result.Errors, err)
			continue
		}

		result.SyncedRules++
		result.TotalCreated += syncResult.Created
		result.TotalUpdated += syncResult.Updated
		result.TotalDeleted += syncResult.Deleted
	}

	log.InfoContextf(ctx, "[TaskPlanner] Sync all completed: total=%d, synced=%d, failed=%d, created=%d, updated=%d, deleted=%d",
		result.TotalRules, result.SyncedRules, result.FailedRules,
		result.TotalCreated, result.TotalUpdated, result.TotalDeleted)
	return result, nil
}

// makeInstanceKey 生成实例唯一键
func (s *TaskPlannerServiceImpl) makeInstanceKey(ruleID, nodeID, symbol string) string {
	return fmt.Sprintf("%s:%s:%s", ruleID, nodeID, symbol)
}

// generateTaskID 生成任务ID
func (s *TaskPlannerServiceImpl) generateTaskID() string {
	sfID := s.snowflake.Generate()
	return strconv.FormatInt(sfID.Int64(), 10)
}
