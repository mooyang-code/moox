package collectmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/planner"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"

	"github.com/bwmarrin/snowflake"
	"trpc.group/trpc-go/trpc-go/log"
)

// TaskPlannerServiceImpl 任务规划器服务实现
type TaskPlannerServiceImpl struct {
	taskRulesDAO dao.CollectorTaskRulesDAO
	instanceDAO  dao.CollectorTaskInstanceDAO
	registry     *planner.PlannerRegistry
	snowflake    *snowflake.Node
	nodeDAO      cloudnodedao.CloudNodeDAO // 新增：用于获取节点列表
}

// NewTaskPlannerServiceImpl 创建任务规划器服务
func NewTaskPlannerServiceImpl(
	taskRulesDAO dao.CollectorTaskRulesDAO,
	instanceDAO dao.CollectorTaskInstanceDAO,
	registry *planner.PlannerRegistry,
	nodeDAO cloudnodedao.CloudNodeDAO, // 新增参数
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
		nodeDAO:      nodeDAO,
	}
}

// SyncRuleInstances 同步指定规则的任务实例（幂等操作）
func (s *TaskPlannerServiceImpl) SyncRuleInstances(ctx context.Context, ruleID string) (*SyncResult, error) {
	log.InfoContextf(ctx, "[TaskPlanner] Starting sync for rule: %s", ruleID)

	result := &SyncResult{RuleID: ruleID}

	// 检查依赖是否初始化
	if s.taskRulesDAO == nil {
		log.ErrorContextf(ctx, "[TaskPlanner] taskRulesDAO is nil")
		return result, fmt.Errorf("taskRulesDAO is not initialized")
	}
	if s.instanceDAO == nil {
		log.ErrorContextf(ctx, "[TaskPlanner] instanceDAO is nil")
		return result, fmt.Errorf("instanceDAO is not initialized")
	}
	if s.registry == nil {
		log.ErrorContextf(ctx, "[TaskPlanner] registry is nil")
		return result, fmt.Errorf("planner registry is not initialized")
	}

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
		NodeTags:       ruleModel.NodeTags,
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
			return nil, fmt.Errorf("planner not found for data type: %s", rule.DataType)
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
// 分配策略：将标的集合均匀分配到节点集合，每个标的只分配到一个节点
func (s *TaskPlannerServiceImpl) computeInstances(ctx context.Context, rule *dto.TaskRuleDTO, dist planner.TaskPlanner) ([]*model.CollectorTaskInstance, error) {
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

	nodeCount := len(nodes)
	log.InfoContextf(ctx, "[TaskPlanner] Distributing %d objects to %d nodes for rule %s",
		len(objects), nodeCount, rule.RuleID)

	// 将标的均匀分配到节点：每个标的只分配到一个节点
	// 使用取模方式实现轮询分配
	for i, object := range objects {
		// 选择节点：轮询分配
		node := nodes[i%nodeCount]

		// 构建任务参数
		params, err := dist.BuildTaskParams(ctx, rule, object)
		if err != nil {
			log.WarnContextf(ctx, "[TaskPlanner] Failed to build task params for %s/%s: %v", node.NodeID, object, err)
			continue
		}

		instance := &model.CollectorTaskInstance{
			RuleID:          rule.RuleID,
			NodeID:          node.NodeID,
			Symbol:          object,
			CollectDataType: rule.DataType, // 填充数据类型字段
			TaskParams:      params,
			Status:          model.InstanceStatusPending,
			Invalid:         model.InvalidNo,
		}
		instances = append(instances, instance)
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

// RecalculateAllTaskInstances 重算所有启用规则的任务实例
// 优化算法：按数据类型分组处理，提高效率
func (s *TaskPlannerServiceImpl) RecalculateAllTaskInstances(ctx context.Context) (*BatchSyncResult, error) {
	log.InfoContext(ctx, "[TaskPlanner] Starting recalculation for all enabled rules")

	result := &BatchSyncResult{}

	// 步骤1：清空所有任务实例表
	log.InfoContext(ctx, "[TaskPlanner] Step 1: Truncating all task instances...")
	if err := s.instanceDAO.TruncateAllInstances(ctx); err != nil {
		return nil, fmt.Errorf("failed to truncate task instances: %w", err)
	}
	log.InfoContext(ctx, "[TaskPlanner] All task instances truncated")

	// 步骤2：获取所有有效的云节点（仅获取在线节点）
	log.InfoContext(ctx, "[TaskPlanner] Step 2: Loading valid nodes...")
	allNodes, err := s.nodeDAO.GetOnlineNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get nodes: %w", err)
	}
	log.InfoContextf(ctx, "[TaskPlanner] Loaded %d online nodes", len(allNodes))

	// 步骤3：提取云节点支持的数据类型并集
	log.InfoContext(ctx, "[TaskPlanner] Step 3: Extracting supported data types...")
	dataTypeSet := make(map[string]bool)
	nodesByDataType := make(map[string][]string) // data_type -> []node_id

	for _, node := range allNodes {
		// 解析 SupportedCollectors JSON 数组
		supportedTypes, err := s.parseSupportedCollectors(node.SupportedCollectors)
		if err != nil {
			log.WarnContextf(ctx, "[TaskPlanner] Failed to parse supported collectors for node %s: %v", node.NodeID, err)
			continue
		}

		for _, dataType := range supportedTypes {
			dataTypeSet[dataType] = true
			nodesByDataType[dataType] = append(nodesByDataType[dataType], node.NodeID)
		}
	}
	log.InfoContextf(ctx, "[TaskPlanner] Found %d unique data types across all nodes", len(dataTypeSet))

	// 步骤4：获取所有启用的规则并按数据类型分组
	log.InfoContext(ctx, "[TaskPlanner] Step 4: Loading enabled rules...")
	rules, err := s.taskRulesDAO.GetTaskRulesList(ctx, "", "", model.EnabledTrue)
	if err != nil {
		return nil, fmt.Errorf("failed to get enabled rules: %w", err)
	}

	result.TotalRules = len(rules)
	log.InfoContextf(ctx, "[TaskPlanner] Loaded %d enabled rules", result.TotalRules)

	// 按数据类型分组规则
	rulesByDataType := make(map[string][]*model.CollectorTaskRules)
	for _, rule := range rules {
		rulesByDataType[rule.DataType] = append(rulesByDataType[rule.DataType], rule)
	}
	log.InfoContextf(ctx, "[TaskPlanner] Rules grouped into %d data types", len(rulesByDataType))

	// 步骤5：按数据类型分组处理
	log.InfoContext(ctx, "[TaskPlanner] Step 5: Processing by data type groups...")
	for dataType := range dataTypeSet {
		typeRules := rulesByDataType[dataType]
		typeNodes := nodesByDataType[dataType]

		if len(typeRules) == 0 {
			log.InfoContextf(ctx, "[TaskPlanner] Data type '%s' has no rules, skipping", dataType)
			continue
		}

		log.InfoContextf(ctx, "[TaskPlanner] Processing data type '%s': %d rules, %d nodes",
			dataType, len(typeRules), len(typeNodes))

		// 处理该数据类型的所有规则
		for _, rule := range typeRules {
			syncResult, err := s.SyncRuleInstances(ctx, rule.RuleID)
			if err != nil {
				log.ErrorContextf(ctx, "[TaskPlanner] Failed to sync rule %s (type=%s): %v",
					rule.RuleID, dataType, err)
				result.FailedRules++
				result.Errors = append(result.Errors, err)
				continue
			}

			result.SyncedRules++
			result.TotalCreated += syncResult.Created
			result.TotalUpdated += syncResult.Updated
			result.TotalDeleted += syncResult.Deleted
		}
	}

	log.InfoContextf(ctx, "[TaskPlanner] Recalculation completed: total=%d, synced=%d, failed=%d, created=%d",
		result.TotalRules, result.SyncedRules, result.FailedRules, result.TotalCreated)
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

// parseSupportedCollectors 解析云节点支持的采集器类型 JSON 数组
// 输入：JSON 数组字符串，如 ["kline", "ticker", "orderbook"]
// 输出：数据类型字符串数组
func (s *TaskPlannerServiceImpl) parseSupportedCollectors(jsonStr string) ([]string, error) {
	if jsonStr == "" || jsonStr == "[]" {
		return []string{}, nil
	}

	var collectors []string
	if err := json.Unmarshal([]byte(jsonStr), &collectors); err != nil {
		return nil, fmt.Errorf("failed to parse supported collectors: %w", err)
	}

	return collectors, nil
}
