package collectmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/planner"

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

// computeInstances 计算应有的实例列表
// 分配策略：
// 1. 已存在且非失败的任务：保留原节点分配，避免任务漂移
// 2. 新任务或失败任务：负载均衡分配到任务最少的节点
func (s *TaskPlannerServiceImpl) computeInstances(
	ctx context.Context,
	rule *dto.TaskRuleDTO,
	dist planner.TaskPlanner,
	existingInstances map[string]*model.CollectorTaskInstance,
	nodeTaskCount map[string]int,
) ([]*model.CollectorTaskInstance, error) {
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

	log.InfoContextf(ctx, "[TaskPlanner] Distributing %d objects to %d nodes for rule %s",
		len(objects), len(nodes), rule.RuleID)

	// 构建节点ID到节点的映射，用于验证节点可用性
	nodeMap := make(map[string]bool)
	for _, node := range nodes {
		nodeMap[node.NodeID] = true
	}

	for _, object := range objects {
		// 构建任务参数
		params, err := dist.BuildTaskParams(ctx, rule, object)
		if err != nil {
			log.WarnContextf(ctx, "[TaskPlanner] Failed to build task params for %s: %v", object, err)
			continue
		}

		// 生成稳定的 TaskID（不包含 nodeID，避免任务漂移）
		taskID := planner.GenerateStableTaskID(rule.RuleID, params)

		var selectedNodeID string

		// 检查是否已有该任务实例
		if existingInst, exists := existingInstances[taskID]; exists {
			// 任务已存在
			if existingInst.Status != model.InstanceStatusFailed {
				// 非失败状态：优先保留原节点分配
				if nodeMap[existingInst.NodeID] {
					// 原节点仍可用，保留
					selectedNodeID = existingInst.NodeID
					log.DebugContextf(ctx, "[TaskPlanner] Instance %s keeping existing node %s",
						taskID[:8], selectedNodeID)
				} else {
					// 原节点不可用，需要重新分配
					selectedNodeID = s.selectLeastLoadedNode(nodes, nodeTaskCount)
					log.InfoContextf(ctx, "[TaskPlanner] Instance %s original node %s unavailable, reassigning to %s",
						taskID[:8], existingInst.NodeID, selectedNodeID)
				}
			} else {
				// 失败状态：重新负载均衡分配
				selectedNodeID = s.selectLeastLoadedNode(nodes, nodeTaskCount)
				log.InfoContextf(ctx, "[TaskPlanner] Instance %s was failed, reassigning to %s",
					taskID[:8], selectedNodeID)
			}
		} else {
			// 新任务：负载均衡分配
			selectedNodeID = s.selectLeastLoadedNode(nodes, nodeTaskCount)
			log.InfoContextf(ctx, "[TaskPlanner] New instance %s assigned to %s",
				taskID[:8], selectedNodeID)
		}

		// 更新节点任务计数
		nodeTaskCount[selectedNodeID]++

		// Symbol 字段直接使用 object 值
		// 如果采集规则没有交易标的信息（object 为空），则 Symbol 为空，表示不按标的负载均衡
		instance := &model.CollectorTaskInstance{
			TaskID:          taskID,
			RuleID:          rule.RuleID,
			NodeID:          selectedNodeID,
			Symbol:          object,
			CollectDataType: rule.DataType,
			TaskParams:      params,
			Status:          model.InstanceStatusPending,
			Invalid:         model.InvalidNo,
		}
		instances = append(instances, instance)
	}

	return instances, nil
}

// selectLeastLoadedNode 选择任务最少的节点（负载均衡）
func (s *TaskPlannerServiceImpl) selectLeastLoadedNode(
	nodes []*cloudnodemodel.CloudNode,
	nodeTaskCount map[string]int,
) string {
	if len(nodes) == 0 {
		return ""
	}

	minNode := nodes[0].NodeID
	minCount := nodeTaskCount[minNode]

	for _, node := range nodes[1:] {
		count := nodeTaskCount[node.NodeID]
		if count < minCount {
			minCount = count
			minNode = node.NodeID
		}
	}

	return minNode
}

// RecalculateAllTaskInstances 重算所有启用规则的任务实例
// 优化算法：差异更新 + 节点健康检查 + 稳定Task ID + 任务漂移防护
// 1. 相同任务（rule_id + task_params 相同）保持相同的task_id，不依赖node_id
// 2. 已存在且非失败的任务实例保留在原节点，避免任务漂移
// 3. 新任务或失败任务使用负载均衡分配到任务最少的节点
// 4. 检查节点健康状态，剔除异常节点的任务
func (s *TaskPlannerServiceImpl) RecalculateAllTaskInstances(ctx context.Context) error {
	log.InfoContext(ctx, "[TaskPlanner] Starting recalculation with diff update, node health check and drift prevention")

	// 步骤1：获取所有现有任务实例
	log.InfoContext(ctx, "[TaskPlanner] Step 1: Loading existing task instances...")
	existingInstances, err := s.instanceDAO.GetAllTaskInstances(ctx)
	if err != nil {
		return fmt.Errorf("failed to get existing instances: %w", err)
	}
	log.InfoContextf(ctx, "[TaskPlanner] Loaded %d existing instances", len(existingInstances))

	// 构建现有实例映射 (新式TaskID -> Instance)，用于 computeInstances 查询
	// 使用新式TaskID（基于 ruleID + taskParams，不含nodeID）作为key
	// 这样可以正确匹配新旧实例，即使旧实例使用的是包含nodeID的旧式TaskID
	existingInstancesMap := make(map[string]*model.CollectorTaskInstance)
	for _, inst := range existingInstances {
		newStyleTaskID := planner.GenerateStableTaskID(inst.RuleID, inst.TaskParams)
		existingInstancesMap[newStyleTaskID] = inst
	}

	// 步骤2：获取所有有效的云节点（仅获取在线节点）
	log.InfoContext(ctx, "[TaskPlanner] Step 2: Loading valid nodes...")
	allNodes, err := s.nodeDAO.GetOnlineNodes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get nodes: %w", err)
	}
	log.InfoContextf(ctx, "[TaskPlanner] Loaded %d online nodes", len(allNodes))

	// 构建节点健康映射（node_id -> true）
	healthyNodeMap := make(map[string]bool)
	for _, node := range allNodes {
		healthyNodeMap[node.NodeID] = true
	}

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
		return fmt.Errorf("failed to get enabled rules: %w", err)
	}

	totalRules := len(rules)
	log.InfoContextf(ctx, "[TaskPlanner] Loaded %d enabled rules", totalRules)

	// 按数据类型分组规则
	rulesByDataType := make(map[string][]*model.CollectorTaskRules)
	for _, rule := range rules {
		rulesByDataType[rule.DataType] = append(rulesByDataType[rule.DataType], rule)
	}
	log.InfoContextf(ctx, "[TaskPlanner] Rules grouped into %d data types", len(rulesByDataType))

	// 步骤5：计算所有应有的实例（负载均衡 + 任务漂移防护）
	log.InfoContext(ctx, "[TaskPlanner] Step 5: Computing all instances with load balancing...")
	var allNewInstances []*model.CollectorTaskInstance
	var syncedRules, failedRules int

	// 节点任务计数，用于负载均衡分配
	nodeTaskCount := make(map[string]int)

	for dataType := range dataTypeSet {
		typeRules := rulesByDataType[dataType]
		typeNodes := nodesByDataType[dataType]

		if len(typeRules) == 0 {
			log.InfoContextf(ctx, "[TaskPlanner] Data type '%s' has no rules, skipping", dataType)
			continue
		}

		log.InfoContextf(ctx, "[TaskPlanner] Processing data type '%s': %d rules, %d nodes",
			dataType, len(typeRules), len(typeNodes))

		// 获取该数据类型的分配器
		dist := s.registry.Get(dataType)
		if dist == nil {
			dist = s.registry.GetOrDefault(dataType)
			if dist == nil {
				log.WarnContextf(ctx, "[TaskPlanner] Planner not found for data type: %s", dataType)
				failedRules += len(typeRules)
				continue
			}
		}

		// 处理该数据类型的所有规则
		for _, ruleModel := range typeRules {
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
			}

			// 计算应有的实例列表（传入现有实例和节点任务计数，实现负载均衡和漂移防护）
			computedInstances, err := s.computeInstances(ctx, rule, dist, existingInstancesMap, nodeTaskCount)
			if err != nil {
				log.ErrorContextf(ctx, "[TaskPlanner] Failed to compute instances for rule %s: %v",
					rule.RuleID, err)
				failedRules++
				continue
			}

			// TaskID 已在 computeInstances 中生成，直接添加到总列表
			allNewInstances = append(allNewInstances, computedInstances...)

			log.InfoContextf(ctx, "[TaskPlanner] Rule %s computed: %d instances",
				rule.RuleID, len(computedInstances))
			syncedRules++
		}
	}

	// 步骤6：差异对比与节点健康检查
	log.InfoContext(ctx, "[TaskPlanner] Step 6: Calculating diff with node health check...")
	toCreate, toUpdate, toDelete := s.calculateDiff(ctx, existingInstances, allNewInstances, healthyNodeMap)

	log.InfoContextf(ctx, "[TaskPlanner] Diff result: create=%d, update=%d, delete=%d",
		len(toCreate), len(toUpdate), len(toDelete))

	// 步骤7：原子性地执行差异更新
	log.InfoContext(ctx, "[TaskPlanner] Step 7: Applying diff update...")
	if err := s.instanceDAO.DiffUpdateInstances(ctx, toCreate, toUpdate, toDelete); err != nil {
		return fmt.Errorf("failed to apply diff update: %w", err)
	}

	log.InfoContextf(ctx, "[TaskPlanner] Recalculation completed: total_rules=%d, synced=%d, failed=%d, create=%d, update=%d, delete=%d",
		totalRules, syncedRules, failedRules, len(toCreate), len(toUpdate), len(toDelete))
	return nil
}

// calculateDiff 计算差异：对比现有实例和应有实例
// 使用 内容键(ruleID + taskParams) 进行匹配，而非直接使用 TaskID
// 这样可以正确处理 TaskID 生成规则变更的情况（旧式包含nodeID，新式不包含）
// 返回：需要创建的、需要更新的、需要删除的实例列表
func (s *TaskPlannerServiceImpl) calculateDiff(
	ctx context.Context,
	existing []*model.CollectorTaskInstance,
	shouldBe []*model.CollectorTaskInstance,
	healthyNodes map[string]bool,
) ([]*model.CollectorTaskInstance, []*model.CollectorTaskInstance, []string) {

	// 构建现有实例映射，使用内容键 (ruleID + taskParams -> instance)
	// 同时保留原始 TaskID 用于删除操作
	type existingInfo struct {
		instance    *model.CollectorTaskInstance
		origTaskID  string
		contentKey  string
	}
	existingByContent := make(map[string]*existingInfo)
	for _, inst := range existing {
		contentKey := planner.GenerateStableTaskID(inst.RuleID, inst.TaskParams)
		existingByContent[contentKey] = &existingInfo{
			instance:   inst,
			origTaskID: inst.TaskID,
			contentKey: contentKey,
		}
	}

	// 用于标记哪些现有实例在新列表中找到了
	foundContentKeys := make(map[string]bool)

	var toCreate []*model.CollectorTaskInstance
	var toUpdate []*model.CollectorTaskInstance

	// 遍历应有的实例
	for _, newInst := range shouldBe {
		// 使用内容键进行匹配（新式TaskID就是内容键）
		contentKey := newInst.TaskID

		if info, exists := existingByContent[contentKey]; exists {
			existingInst := info.instance
			// 任务已存在（按内容匹配）
			foundContentKeys[contentKey] = true

			// 检查是否需要更新
			// 1. 节点ID变化（失败任务被重新分配）
			// 2. TaskID变化（从旧式迁移到新式）
			needUpdate := false
			if existingInst.NodeID != newInst.NodeID {
				log.InfoContextf(ctx, "[TaskPlanner] Instance %s node changed (node: %s->%s)",
					contentKey[:8], existingInst.NodeID, newInst.NodeID)
				needUpdate = true
			}
			if info.origTaskID != newInst.TaskID {
				log.InfoContextf(ctx, "[TaskPlanner] Instance %s TaskID migrated (old: %s, new: %s)",
					contentKey[:8], info.origTaskID[:8], newInst.TaskID[:8])
				needUpdate = true
			}

			if needUpdate {
				// 保留执行状态
				newInst.Status = existingInst.Status
				newInst.LastExecTime = existingInst.LastExecTime
				newInst.Result = existingInst.Result
				toUpdate = append(toUpdate, newInst)
			}
			// 如果不需要更新，则保持原样（不添加到任何列表）
		} else {
			// 新任务
			toCreate = append(toCreate, newInst)
			log.InfoContextf(ctx, "[TaskPlanner] New instance: %s (node: %s, rule: %s)",
				newInst.TaskID[:8], newInst.NodeID, newInst.RuleID)
		}
	}

	// 查找需要删除的实例
	var toDelete []string
	for contentKey, info := range existingByContent {
		if !foundContentKeys[contentKey] {
			// 实例不在新列表中，需要删除
			toDelete = append(toDelete, info.origTaskID)
			log.InfoContextf(ctx, "[TaskPlanner] Instance %s will be deleted (no longer needed)",
				info.origTaskID[:8])
		} else if !healthyNodes[info.instance.NodeID] {
			// 节点已不健康，删除该任务（即使在新列表中，会被重新创建到健康节点）
			toDelete = append(toDelete, info.origTaskID)
			log.WarnContextf(ctx, "[TaskPlanner] Instance %s will be deleted (node %s is unhealthy)",
				info.origTaskID[:8], info.instance.NodeID)
		}
	}

	return toCreate, toUpdate, toDelete
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
