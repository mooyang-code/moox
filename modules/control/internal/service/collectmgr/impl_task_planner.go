package collectmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	cloudnodedao "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/dao"
	cloudnodemodel "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/model"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/planner"

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
	memStore     TaskInstanceStore // 内存任务实例仓库
}

// NewTaskPlannerServiceImpl 创建任务规划器服务
func NewTaskPlannerServiceImpl(
	taskRulesDAO dao.CollectorTaskRulesDAO,
	instanceDAO dao.CollectorTaskInstanceDAO,
	registry *planner.PlannerRegistry,
	nodeDAO cloudnodedao.CloudNodeDAO, // 新增参数
	onlineNodes planner.OnlineNodeIDsProvider,
	memStore TaskInstanceStore, // 内存仓库
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
		memStore:     memStore,
	}
}

// selectNodeForTask 为任务选择执行节点
// 简化策略：直接负载均衡分配到任务最少的节点
func (s *TaskPlannerServiceImpl) selectNodeForTask(
	ctx context.Context,
	taskID string,
	ruleID string,
	nodes []*cloudnodemodel.CloudNode,
	nodeTaskCount map[string]int,
) string {
	nodeID := s.selectLeastLoadedNode(nodes, nodeTaskCount)
	log.DebugContextf(ctx, "[TaskPlanner] Task %s assigned to %s (ruleID=%s)",
		taskID, nodeID, ruleID)
	return nodeID
}

// createInstanceForObject 为单个对象创建任务实例
// 简化版本：不继承历史状态，每次都是全新分配
func (s *TaskPlannerServiceImpl) createInstanceForObject(
	ctx context.Context,
	rule *dto.TaskRuleDTO,
	object string,
	dist planner.TaskPlanner,
	nodes []*cloudnodemodel.CloudNode,
	nodeTaskCount map[string]int,
) (*model.CollectorTaskInstance, error) {
	// 1. 构建任务参数
	params, err := dist.BuildTaskParams(ctx, rule, object)
	if err != nil {
		return nil, fmt.Errorf("failed to build task params: %w", err)
	}

	// 2. 生成稳定的TaskID（不包含nodeID，避免任务漂移）
	taskID := planner.GenerateStableTaskID(rule.RuleID, params)

	// 3. 选择执行节点（负载均衡分配）
	selectedNodeID := s.selectNodeForTask(ctx, taskID, rule.RuleID, nodes, nodeTaskCount)

	// 4. 更新节点任务计数
	nodeTaskCount[selectedNodeID]++

	// 5. 提取 symbol 和 interval
	symbol, interval := s.parseObject(object, rule.DataType, params)

	// 6. 构建实例对象（v2.0: 使用新字段）
	instance := &model.CollectorTaskInstance{
		TaskID:          taskID,
		RuleID:          rule.RuleID,
		BizType:         rule.BizType,
		PlannedExecNode: selectedNodeID, // 计划执行节点
		Symbol:          symbol,
		CollectDataType: rule.DataType,
		Interval:        interval,
		TaskParams:      params,
		LastExecStatus:  model.InstanceStatusPending, // 初始状态
		Invalid:         model.InvalidNo,
	}

	return instance, nil
}

// parseObject 从对象和任务参数中提取symbol和interval
// 对于K线任务：object格式为 "symbol@interval"
// 对于其他任务：object就是symbol，interval为"default"
func (s *TaskPlannerServiceImpl) parseObject(object string, dataType string, taskParams string) (string, string) {
	// K线任务使用 @ 分隔符
	if dataType == model.DataTypeKline {
		for i := len(object) - 1; i >= 0; i-- {
			if object[i] == '@' {
				symbol := object[:i]
				interval := object[i+1:]
				return symbol, interval
			}
		}
	}

	// 非K线任务：尝试从TaskParams中提取第一个interval
	// 解析 TaskParams JSON
	var params struct {
		Intervals []string `json:"intervals"`
	}
	if err := json.Unmarshal([]byte(taskParams), &params); err == nil && len(params.Intervals) > 0 {
		// 有intervals配置（如Symbol任务），使用第一个interval
		return object, params.Intervals[0]
	}

	// 默认情况：interval为"default"
	return object, "default"
}

// computeInstances 计算应有的实例列表
// 简化策略：所有任务均负载均衡分配到任务最少的节点
func (s *TaskPlannerServiceImpl) computeInstances(
	ctx context.Context,
	rule *dto.TaskRuleDTO,
	dist planner.TaskPlanner,
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

	// 3. 为每个对象创建实例
	var instances []*model.CollectorTaskInstance
	for _, object := range objects {
		instance, err := s.createInstanceForObject(ctx, rule, object, dist, nodes, nodeTaskCount)
		if err != nil {
			log.WarnContextf(ctx, "[TaskPlanner] Failed to create instance for %s (ruleID=%s): %v",
				object, rule.RuleID, err)
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

// loadOnlineNodes 加载在线节点
// 实现：从DB加载所有节点，然后内存过滤出在线节点
// 根据执行计划要求：只有Online状态的节点可分配，Timeout节点也不可分配
func (s *TaskPlannerServiceImpl) loadOnlineNodes(ctx context.Context) ([]*cloudnodemodel.CloudNode, error) {
	log.InfoContext(ctx, "[TaskPlanner] Step 1: Loading online nodes...")

	// 1. 从DB获取所有节点
	allNodesFromDB, err := s.nodeDAO.GetAllNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get all nodes: %w", err)
	}
	log.InfoContextf(ctx, "[TaskPlanner] Loaded %d total nodes from DB", len(allNodesFromDB))

	// 2. 获取在线节点ID列表（内存操作）
	if s.onlineNodes == nil {
		return nil, fmt.Errorf("online node provider not set")
	}
	onlineNodeIDs := s.onlineNodes.GetOnlineNodeIDs()
	log.InfoContextf(ctx, "[TaskPlanner] Got %d online node IDs from provider", len(onlineNodeIDs))

	// 3. 构建在线节点ID集合（用于O(1)查找）
	onlineIDSet := make(map[string]bool, len(onlineNodeIDs))
	for _, nodeID := range onlineNodeIDs {
		onlineIDSet[nodeID] = true
	}

	// 4. 过滤出在线节点（只有Online状态的节点）
	onlineNodes := make([]*cloudnodemodel.CloudNode, 0, len(onlineNodeIDs))
	for _, node := range allNodesFromDB {
		if onlineIDSet[node.NodeID] {
			onlineNodes = append(onlineNodes, node)
		}
	}

	log.InfoContextf(ctx, "[TaskPlanner] Filtered to %d online nodes", len(onlineNodes))
	return onlineNodes, nil
}

// extractDataTypes 提取节点支持的数据类型并分组
func (s *TaskPlannerServiceImpl) extractDataTypes(
	ctx context.Context,
	nodes []*cloudnodemodel.CloudNode,
) (map[string]bool, map[string][]string, error) {
	log.InfoContext(ctx, "[TaskPlanner] Step 2: Extracting supported data types...")

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
	log.InfoContext(ctx, "[TaskPlanner] Step 3: Loading enabled rules...")

	// 获取所有启用的规则
	rules, err := s.taskRulesDAO.GetTaskRulesList(ctx, "", "", "", model.EnabledTrue)
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
// 以规则为驱动遍历，避免因节点 SupportedCollectors 未包含某数据类型
// 而导致对应规则被整体跳过（如 symbol 类型规则）
func (s *TaskPlannerServiceImpl) computeAllInstances(
	ctx context.Context,
	_ map[string]bool,
	_ map[string][]string,
	rulesByDataType map[string][]*model.CollectorTaskRules,
) ([]*model.CollectorTaskInstance, int, int, error) {
	log.InfoContext(ctx, "[TaskPlanner] Step 4: Computing all instances with load balancing...")

	var allNewInstances []*model.CollectorTaskInstance
	var syncedRules, failedRules int

	// 节点任务计数，用于负载均衡
	nodeTaskCount := make(map[string]int)

	// 以规则为驱动：遍历所有数据类型的规则
	// 不依赖 dataTypeSet（节点侧的数据类型集合），避免节点未声明某类型时规则被跳过
	for dataType, typeRules := range rulesByDataType {
		if len(typeRules) == 0 {
			continue
		}

		// 获取该数据类型的分配器
		dist := s.getDistributor(ctx, dataType)
		if dist == nil {
			log.WarnContextf(ctx, "[TaskPlanner] No distributor for data type: %s", dataType)
			failedRules += len(typeRules)
			continue
		}

		log.InfoContextf(ctx, "[TaskPlanner] Processing data type '%s': %d rules", dataType, len(typeRules))

		// 处理该数据类型的所有规则
		for _, ruleModel := range typeRules {
			rule := s.convertRuleToDTO(ruleModel)

			computedInstances, err := s.computeInstances(ctx, rule, dist, nodeTaskCount)
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
		BizType:        ruleModel.BizType,
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
// 流程：
// 1. 加载在线节点（仅Online状态）
// 2. 提取支持的数据类型
// 3. 加载启用的规则
// 4. 计算所有任务实例（负载均衡分配）
// 5. 全量覆盖写入内存（原子操作）
// 6. 异步刷新到数据库
func (s *TaskPlannerServiceImpl) RecalculateAllTaskInstances(ctx context.Context) error {
	startTime := time.Now()
	log.InfoContext(ctx, "[TaskPlanner] Starting recalculation...")

	// 步骤1：加载在线节点（只有Online状态的节点可分配）
	onlineNodes, err := s.loadOnlineNodes(ctx)
	if err != nil {
		return err
	}

	// 步骤2：提取支持的数据类型
	dataTypeSet, nodesByDataType, err := s.extractDataTypes(ctx, onlineNodes)
	if err != nil {
		return err
	}

	// 步骤3：加载启用的规则
	rules, rulesByDataType, err := s.loadEnabledRules(ctx)
	if err != nil {
		return err
	}

	// 步骤4：计算所有任务实例（负载均衡分配）
	allNewInstances, syncedCount, failedCount, err := s.computeAllInstances(
		ctx, dataTypeSet, nodesByDataType, rulesByDataType)
	if err != nil {
		return err
	}

	// 步骤5：覆盖写入内存
	s.memStore.ReplaceAll(ctx, allNewInstances)

	// 步骤6：刷DB
	s.flushToDB(ctx, allNewInstances)

	elapsed := time.Since(startTime)
	log.InfoContextf(ctx, "[TaskPlanner] Recalculation completed in %v: total_rules=%d, "+
		"synced=%d, failed=%d, instances=%d, version=%d",
		elapsed, len(rules), syncedCount, failedCount, len(allNewInstances), s.memStore.GetVersion())

	return nil
}

// flushToDB 异步刷库
// 将内存中的任务实例全量替换到数据库（Truncate + BatchCreate）
func (s *TaskPlannerServiceImpl) flushToDB(ctx context.Context, instances []*model.CollectorTaskInstance) {
	if len(instances) == 0 {
		log.InfoContext(ctx, "[TaskPlanner] Skip DB flush: no instances")
		return
	}

	startTime := time.Now()
	log.InfoContextf(ctx, "[TaskPlanner] Starting async DB flush: %d instances", len(instances))

	s.logDuplicateTaskIDs(ctx, instances)

	// 使用 TruncateAndBatchCreate 全量替换
	if err := s.instanceDAO.TruncateAndBatchCreate(ctx, instances); err != nil {
		log.ErrorContextf(ctx, "[TaskPlanner] Async DB flush failed: %v", err)
		return
	}

	elapsed := time.Since(startTime)
	log.InfoContextf(ctx, "[TaskPlanner] Async DB flush completed in %v: %d instances",
		elapsed, len(instances))
}

func (s *TaskPlannerServiceImpl) logDuplicateTaskIDs(ctx context.Context, instances []*model.CollectorTaskInstance) {
	type dupInfo struct {
		count  int
		ruleID string
		params string
	}

	dupMap := make(map[string]dupInfo, len(instances))
	for _, inst := range instances {
		if inst == nil || inst.TaskID == "" {
			continue
		}
		if info, ok := dupMap[inst.TaskID]; ok {
			info.count++
			dupMap[inst.TaskID] = info
			continue
		}
		dupMap[inst.TaskID] = dupInfo{
			count:  1,
			ruleID: inst.RuleID,
			params: inst.TaskParams,
		}
	}

	dupTotal := 0
	for taskID, info := range dupMap {
		if info.count <= 1 {
			continue
		}
		dupTotal++
		log.WarnContextf(ctx, "[TaskPlanner] Duplicate TaskID detected: task_id=%s, count=%d, rule_id=%s, task_params=%s",
			taskID, info.count, info.ruleID, info.params)
	}
	if dupTotal > 0 {
		log.WarnContextf(ctx, "[TaskPlanner] Total duplicate TaskIDs: %d", dupTotal)
	}
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
