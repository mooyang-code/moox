package collectmgr

import (
	"context"
	"encoding/json"
	"fmt"
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
	onlineNodes  planner.OnlineNodeIDsProvider
}

// NewTaskPlannerServiceImpl 创建任务规划器服务
func NewTaskPlannerServiceImpl(
	taskRulesDAO dao.CollectorTaskRulesDAO,
	instanceDAO dao.CollectorTaskInstanceDAO,
	registry *planner.PlannerRegistry,
	nodeDAO cloudnodedao.CloudNodeDAO, // 新增参数
	onlineNodes planner.OnlineNodeIDsProvider,
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
		onlineNodes:  onlineNodes,
	}
}

// buildNodeMap 构建节点ID映射表
func (s *TaskPlannerServiceImpl) buildNodeMap(nodes []*cloudnodemodel.CloudNode) map[string]bool {
	nodeMap := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		nodeMap[node.NodeID] = true
	}
	return nodeMap
}

// selectNodeForTask 为任务选择执行节点
// 使用卫语句处理不同场景，消除if嵌套
// 分配策略：
// 1. 新任务：负载均衡分配
// 2. 失败任务：重新负载均衡分配
// 3. 原节点不可用：重新负载均衡分配
// 4. 默认：保留原节点（避免任务漂移）
func (s *TaskPlannerServiceImpl) selectNodeForTask(
	ctx context.Context,
	taskID string,
	nodes []*cloudnodemodel.CloudNode,
	nodeMap map[string]bool,
	existingInstances map[string]*model.CollectorTaskInstance,
	nodeTaskCount map[string]int,
) string {
	existingInst, exists := existingInstances[taskID]

	// 1：任务不存在，新任务分配
	if !exists {
		nodeID := s.selectLeastLoadedNode(nodes, nodeTaskCount)
		log.InfoContextf(ctx, "[TaskPlanner] New instance %s assigned to %s",
			taskID[:8], nodeID)
		return nodeID
	}

	// 2：任务失败，重新分配
	if existingInst.Status == model.InstanceStatusFailed {
		nodeID := s.selectLeastLoadedNode(nodes, nodeTaskCount)
		log.InfoContextf(ctx, "[TaskPlanner] Instance %s was failed, reassigning to %s",
			taskID[:8], nodeID)
		return nodeID
	}

	// 3：原节点不可用，重新分配
	if !nodeMap[existingInst.NodeID] {
		nodeID := s.selectLeastLoadedNode(nodes, nodeTaskCount)
		log.InfoContextf(ctx, "[TaskPlanner] Instance %s original node %s unavailable, reassigning to %s",
			taskID[:8], existingInst.NodeID, nodeID)
		return nodeID
	}

	// 默认情况：保留原节点（避免任务漂移）
	log.DebugContextf(ctx, "[TaskPlanner] Instance %s keeping existing node %s",
		taskID[:8], existingInst.NodeID)
	return existingInst.NodeID
}

// createInstanceForObject 为单个对象创建任务实例
func (s *TaskPlannerServiceImpl) createInstanceForObject(
	ctx context.Context,
	rule *dto.TaskRuleDTO,
	object string,
	dist planner.TaskPlanner,
	nodes []*cloudnodemodel.CloudNode,
	nodeMap map[string]bool,
	existingInstances map[string]*model.CollectorTaskInstance,
	nodeTaskCount map[string]int,
) (*model.CollectorTaskInstance, error) {
	// 1. 构建任务参数
	params, err := dist.BuildTaskParams(ctx, rule, object)
	if err != nil {
		return nil, fmt.Errorf("failed to build task params: %w", err)
	}

	// 2. 生成稳定的TaskID（不包含nodeID，避免任务漂移）
	taskID := planner.GenerateStableTaskID(rule.RuleID, params)

	// 3. 选择执行节点
	selectedNodeID := s.selectNodeForTask(ctx, taskID, nodes, nodeMap,
		existingInstances, nodeTaskCount)

	// 4. 更新节点任务计数
	nodeTaskCount[selectedNodeID]++

	// 5. 构建实例对象
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

	return instance, nil
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
	// 1. 获取匹配的节点
	nodes, err := dist.GetMatchingNodes(ctx, rule)
	if err != nil {
		return nil, fmt.Errorf("failed to get matching nodes: %w", err)
	}
	if len(nodes) == 0 {
		log.InfoContextf(ctx, "[TaskPlanner] No matching nodes for rule %s", rule.RuleID)
		return nil, nil
	}

	// 2. 获取目标对象（交易标的）
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

	// 3. 构建节点映射（用于快速查找节点是否可用）
	nodeMap := s.buildNodeMap(nodes)

	// 4. 为每个对象创建实例
	var instances []*model.CollectorTaskInstance
	for _, object := range objects {
		instance, err := s.createInstanceForObject(ctx, rule, object, dist,
			nodes, nodeMap, existingInstances, nodeTaskCount)
		if err != nil {
			log.WarnContextf(ctx, "[TaskPlanner] Failed to create instance for %s: %v",
				object, err)
			continue
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

// ========== RecalculateAllTaskInstances 辅助方法 ==========

// loadExistingInstances 加载所有现有任务实例并构建映射
func (s *TaskPlannerServiceImpl) loadExistingInstances(ctx context.Context) (
	[]*model.CollectorTaskInstance,
	map[string]*model.CollectorTaskInstance,
	error,
) {
	log.InfoContext(ctx, "[TaskPlanner] Step 1: Loading existing task instances...")

	// 获取所有实例
	instances, err := s.instanceDAO.GetAllTaskInstances(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get existing instances: %w", err)
	}

	// 构建实例映射（使用新式TaskID作为key）
	instancesMap := make(map[string]*model.CollectorTaskInstance, len(instances))
	for _, inst := range instances {
		newStyleTaskID := planner.GenerateStableTaskID(inst.RuleID, inst.TaskParams)
		instancesMap[newStyleTaskID] = inst
	}

	log.InfoContextf(ctx, "[TaskPlanner] Loaded %d existing instances", len(instances))
	return instances, instancesMap, nil
}

// loadOnlineNodes 加载在线节点
// 实现：从DB加载所有节点，然后内存过滤出在线节点
// 优势：减少DB查询复杂度（全表扫描 vs WHERE IN）
func (s *TaskPlannerServiceImpl) loadOnlineNodes(ctx context.Context) (
	[]*cloudnodemodel.CloudNode,
	map[string]bool,
	error,
) {
	log.InfoContext(ctx, "[TaskPlanner] Step 2: Loading online nodes...")

	// 1. 从DB获取所有节点
	allNodesFromDB, err := s.nodeDAO.GetAllNodes(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get all nodes: %w", err)
	}
	log.InfoContextf(ctx, "[TaskPlanner] Loaded %d total nodes from DB", len(allNodesFromDB))

	// 2. 获取在线节点ID列表（内存操作）
	if s.onlineNodes == nil {
		return nil, nil, fmt.Errorf("online node provider not set")
	}
	onlineNodeIDs := s.onlineNodes.GetOnlineNodeIDs()
	log.InfoContextf(ctx, "[TaskPlanner] Got %d online node IDs from provider", len(onlineNodeIDs))

	// 3. 构建在线节点ID集合（用于O(1)查找）
	onlineIDSet := make(map[string]bool, len(onlineNodeIDs))
	for _, nodeID := range onlineNodeIDs {
		onlineIDSet[nodeID] = true
	}

	// 4. 过滤出在线节点
	onlineNodes := make([]*cloudnodemodel.CloudNode, 0, len(onlineNodeIDs))
	for _, node := range allNodesFromDB {
		if onlineIDSet[node.NodeID] {
			onlineNodes = append(onlineNodes, node)
		}
	}

	// 5. 构建健康节点映射
	healthyNodeMap := make(map[string]bool, len(onlineNodes))
	for _, node := range onlineNodes {
		healthyNodeMap[node.NodeID] = true
	}

	log.InfoContextf(ctx, "[TaskPlanner] Filtered to %d online nodes", len(onlineNodes))
	return onlineNodes, healthyNodeMap, nil
}

// extractDataTypes 提取节点支持的数据类型并分组
func (s *TaskPlannerServiceImpl) extractDataTypes(
	ctx context.Context,
	nodes []*cloudnodemodel.CloudNode,
) (map[string]bool, map[string][]string, error) {
	log.InfoContext(ctx, "[TaskPlanner] Step 3: Extracting supported data types...")

	dataTypeSet := make(map[string]bool)
	nodesByDataType := make(map[string][]string)

	for _, node := range nodes {
		// 解析节点支持的采集器类型
		supportedTypes, err := s.parseSupportedCollectors(node.SupportedCollectors)
		if err != nil {
			log.WarnContextf(ctx, "[TaskPlanner] Failed to parse collectors for node %s: %v",
				node.NodeID, err)
			continue
		}

		// 添加到集合和映射
		for _, dataType := range supportedTypes {
			dataTypeSet[dataType] = true
			nodesByDataType[dataType] = append(nodesByDataType[dataType], node.NodeID)
		}
	}

	log.InfoContextf(ctx, "[TaskPlanner] Found %d unique data types", len(dataTypeSet))
	return dataTypeSet, nodesByDataType, nil
}

// loadEnabledRules 加载所有启用的规则并按数据类型分组
func (s *TaskPlannerServiceImpl) loadEnabledRules(ctx context.Context) (
	[]*model.CollectorTaskRules,
	map[string][]*model.CollectorTaskRules,
	error,
) {
	log.InfoContext(ctx, "[TaskPlanner] Step 4: Loading enabled rules...")

	// 获取所有启用的规则
	rules, err := s.taskRulesDAO.GetTaskRulesList(ctx, "", "", model.EnabledTrue)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get enabled rules: %w", err)
	}

	// 按数据类型分组
	rulesByDataType := make(map[string][]*model.CollectorTaskRules)
	for _, rule := range rules {
		rulesByDataType[rule.DataType] = append(rulesByDataType[rule.DataType], rule)
	}

	log.InfoContextf(ctx, "[TaskPlanner] Loaded %d enabled rules grouped into %d types",
		len(rules), len(rulesByDataType))
	return rules, rulesByDataType, nil
}

// computeAllInstances 计算所有应有的任务实例
// 流程：
//  1. 遍历每个数据类型
//  2. 获取该类型的规则和节点
//  3. 对每个规则：
//     a. 获取匹配节点（GetMatchingNodes）
//     b. 获取交易标的（GetTargetObjects） ← 关键步骤
//     c. 为每个标的创建任务实例
//     d. 负载均衡分配到节点
//  4. 任务漂移防护：已存在的非失败任务保留原节点
func (s *TaskPlannerServiceImpl) computeAllInstances(
	ctx context.Context,
	dataTypeSet map[string]bool,
	nodesByDataType map[string][]string,
	rulesByDataType map[string][]*model.CollectorTaskRules,
	existingInstancesMap map[string]*model.CollectorTaskInstance,
) ([]*model.CollectorTaskInstance, int, int, error) {
	log.InfoContext(ctx, "[TaskPlanner] Step 5: Computing all instances with load balancing...")

	var allNewInstances []*model.CollectorTaskInstance
	var syncedRules, failedRules int

	// 节点任务计数，用于负载均衡
	nodeTaskCount := make(map[string]int)

	// 遍历每个数据类型
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
		dist := s.getDistributor(ctx, dataType)
		if dist == nil {
			log.WarnContextf(ctx, "[TaskPlanner] No distributor for data type: %s", dataType)
			failedRules += len(typeRules)
			continue
		}

		// 处理该数据类型的所有规则
		for _, ruleModel := range typeRules {
			// 转换为DTO
			rule := s.convertRuleToDTO(ruleModel)

			// 计算该规则的任务实例
			// 注：在 computeInstances 内部会：
			//   1. 调用 GetTargetObjects 获取交易标的
			//   2. 为每个标的创建任务实例
			//   3. 负载均衡分配到节点
			computedInstances, err := s.computeInstances(ctx, rule, dist,
				existingInstancesMap, nodeTaskCount)
			if err != nil {
				log.ErrorContextf(ctx, "[TaskPlanner] Failed to compute instances for rule %s: %v",
					rule.RuleID, err)
				failedRules++
				continue
			}

			allNewInstances = append(allNewInstances, computedInstances...)
			log.InfoContextf(ctx, "[TaskPlanner] Rule %s computed: %d instances",
				rule.RuleID, len(computedInstances))
			syncedRules++
		}
	}

	log.InfoContextf(ctx, "[TaskPlanner] Computed %d total instances (synced: %d, failed: %d)",
		len(allNewInstances), syncedRules, failedRules)
	return allNewInstances, syncedRules, failedRules, nil
}

// applyDiffUpdate 应用差异更新
func (s *TaskPlannerServiceImpl) applyDiffUpdate(
	ctx context.Context,
	existingInstances []*model.CollectorTaskInstance,
	shouldBeInstances []*model.CollectorTaskInstance,
	healthyNodeMap map[string]bool,
) error {
	log.InfoContext(ctx, "[TaskPlanner] Step 6: Calculating diff with node health check...")

	// 计算差异
	toCreate, toUpdate, toDelete := s.calculateDiff(ctx, existingInstances,
		shouldBeInstances, healthyNodeMap)

	log.InfoContextf(ctx, "[TaskPlanner] Diff result: create=%d, update=%d, delete=%d",
		len(toCreate), len(toUpdate), len(toDelete))

	// 执行差异更新
	log.InfoContext(ctx, "[TaskPlanner] Step 7: Applying diff update...")
	if err := s.instanceDAO.DiffUpdateInstances(ctx, toCreate, toUpdate, toDelete); err != nil {
		return fmt.Errorf("failed to apply diff update: %w", err)
	}

	log.InfoContext(ctx, "[TaskPlanner] Diff update applied successfully")
	return nil
}

// getDistributor 获取数据类型对应的分配器
func (s *TaskPlannerServiceImpl) getDistributor(
	ctx context.Context,
	dataType string,
) planner.TaskPlanner {
	dist := s.registry.Get(dataType)
	if dist == nil {
		dist = s.registry.GetOrDefault(dataType)
		if dist == nil {
			log.WarnContextf(ctx, "[TaskPlanner] Planner not found for data type: %s", dataType)
			return nil
		}
	}
	return dist
}

// convertRuleToDTO 将规则Model转换为DTO
func (s *TaskPlannerServiceImpl) convertRuleToDTO(
	ruleModel *model.CollectorTaskRules,
) *dto.TaskRuleDTO {
	return &dto.TaskRuleDTO{
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
}

// RecalculateAllTaskInstances 重算所有启用规则的任务实例
// 优化算法：差异更新 + 节点健康检查 + 稳定Task ID + 任务漂移防护
// 1. 相同任务（rule_id + task_params 相同）保持相同的task_id，不依赖node_id
// 2. 已存在且非失败的任务实例保留在原节点，避免任务漂移
// 3. 新任务或失败任务使用负载均衡分配到任务最少的节点
// 4. 检查节点健康状态，剔除异常节点的任务
func (s *TaskPlannerServiceImpl) RecalculateAllTaskInstances(ctx context.Context) error {
	log.InfoContext(ctx, "[TaskPlanner] Starting recalculation with diff update, "+
		"node health check and drift prevention")

	// 步骤1：加载现有任务实例
	existingInstances, existingMap, err := s.loadExistingInstances(ctx)
	if err != nil {
		return err
	}

	// 步骤2：加载在线节点
	onlineNodes, healthyNodeMap, err := s.loadOnlineNodes(ctx)
	if err != nil {
		return err
	}

	// 步骤3：提取支持的数据类型
	dataTypeSet, nodesByDataType, err := s.extractDataTypes(ctx, onlineNodes)
	if err != nil {
		return err
	}

	// 步骤4：加载启用的规则
	rules, rulesByDataType, err := s.loadEnabledRules(ctx)
	if err != nil {
		return err
	}

	// 步骤5：计算应有实例
	// 注：在此步骤中，会为每个规则获取交易标的（GetTargetObjects）并分配到节点
	allNewInstances, syncedCount, failedCount, err := s.computeAllInstances(
		ctx, dataTypeSet, nodesByDataType, rulesByDataType, existingMap)
	if err != nil {
		return err
	}

	// 步骤6：应用差异更新
	if err := s.applyDiffUpdate(ctx, existingInstances, allNewInstances, healthyNodeMap); err != nil {
		return err
	}

	log.InfoContextf(ctx, "[TaskPlanner] Recalculation completed: total_rules=%d, "+
		"synced=%d, failed=%d", len(rules), syncedCount, failedCount)
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
		instance   *model.CollectorTaskInstance
		origTaskID string
		contentKey string
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
