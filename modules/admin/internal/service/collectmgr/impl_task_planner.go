package collectmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/common"
	cloudnodedao "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/dao"
	cloudnodemodel "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/planner"

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
// 分配策略（稳定优先，避免任务漂移）：
//  1. fixed 单节点：强绑定该节点，跳过负载均衡与哈希
//  2. 旧分配仍在候选节点集合内：保留旧节点（D2/D4，节点未变化不迁移）
//  3. 新任务或旧节点已失效：Rendezvous Hash 在候选节点内稳定分配（D3）
//
// 候选节点集合由调用方传入；fixed 单节点场景 candidates 即为该单节点。
// nodeTaskCount 用于记录负载，仅做观测，不再影响分配结果（Rendezvous 自身稳定）。
func (s *TaskPlannerServiceImpl) selectNodeForTask(
	ctx context.Context,
	taskID string,
	rule *model.CollectorTaskRules,
	candidates []*cloudnodemodel.CloudNode,
	oldNodeID string,
	nodeTaskCount map[string]int,
) string {
	if len(candidates) == 0 {
		return ""
	}

	// C1: fixed 单节点强绑定
	if rule.AssignmentType == model.AssignmentTypeFixed && len(candidates) == 1 {
		nodeID := candidates[0].NodeID
		log.DebugContextf(ctx, "[TaskPlanner] Task %s fixed-bound to %s (ruleID=%s)",
			taskID, nodeID, rule.RuleID)
		return nodeID
	}

	// D2/D4: 旧分配仍在候选集合内则保留，避免节点未变化时的无谓迁移
	if oldNodeID != "" {
		for _, n := range candidates {
			if n.NodeID == oldNodeID {
				log.DebugContextf(ctx, "[TaskPlanner] Task %s keep old node %s (ruleID=%s)",
					taskID, oldNodeID, rule.RuleID)
				return oldNodeID
			}
		}
	}

	// D3: Rendezvous Hash 在候选节点内稳定分配
	// 先做字典序稳定排序（D1），消除节点输入顺序不确定性
	nodeIDs := make([]string, 0, len(candidates))
	for _, n := range candidates {
		nodeIDs = append(nodeIDs, n.NodeID)
	}
	sortedIDs := planner.SortNodeIDs(nodeIDs)
	selected := planner.RendezvousHash(taskID, sortedIDs)

	log.DebugContextf(ctx, "[TaskPlanner] Task %s rendezvous-assigned to %s (ruleID=%s, oldNode=%s)",
		taskID, selected, rule.RuleID, oldNodeID)
	return selected
}

// createInstanceForObject 为单个对象创建任务实例
// 整合：B5 实例带 SpaceID、B6 task_id 含 space_id、D1 对象稳定排序由上层保证、
//
//	D2 优先保留旧分配、C1/C2 fixed 强绑定（通过 selectNodeForTask 分派）
func (s *TaskPlannerServiceImpl) createInstanceForObject(
	ctx context.Context,
	rule *model.CollectorTaskRules,
	object string,
	dist planner.TaskPlanner,
	nodes []*cloudnodemodel.CloudNode,
	oldAssignment map[string]string,
	nodeTaskCount map[string]int,
) (*model.CollectorTaskInstance, error) {
	// 1. 构建任务参数
	params, err := dist.BuildTaskParams(ctx, rule, object)
	if err != nil {
		return nil, fmt.Errorf("failed to build task params: %w", err)
	}

	// 2. 生成稳定的TaskID（含 space_id，避免跨空间碰撞；不含 node_id，避免任务漂移）
	taskID := planner.GenerateStableTaskID(rule.SpaceID, rule.RuleID, params)

	// 3. 选择执行节点（保留旧分配优先，其次 Rendezvous）
	oldNodeID := oldAssignment[taskID]
	selectedNodeID := s.selectNodeForTask(ctx, taskID, rule, nodes, oldNodeID, nodeTaskCount)
	if selectedNodeID == "" {
		return nil, fmt.Errorf("no selectable node for task %s (ruleID=%s)", taskID, rule.RuleID)
	}

	// 4. 更新节点任务计数（仅观测用）
	nodeTaskCount[selectedNodeID]++

	// 5. 提取 symbol 和 interval
	symbol, interval := s.parseObject(object, rule.DataType, params)

	// 6. 构建实例对象（携带 SpaceID）
	instance := &model.CollectorTaskInstance{
		SpaceID:         rule.SpaceID,
		TaskID:          taskID,
		RuleID:          rule.RuleID,
		BizType:         rule.BizType,
		PlannedExecNode: selectedNodeID,
		Symbol:          symbol,
		CollectDataType: rule.DataType,
		Interval:        interval,
		TaskParams:      params,
		LastExecStatus:  model.InstanceStatusPending,
		IsDeleted:       common.IsDeletedFalse,
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

// computeInstances 计算单条规则应有的实例列表
// 整合：
//   - D1：对象（symbol/interval）按字典序稳定排序，消除输入顺序敏感性
//   - D2：oldAssignment 传入，优先保留旧 planned_exec_node
//   - C2：fixed 节点校验已在 GetMatchingNodes 上层完成，此处 nodes 即校验通过的候选
func (s *TaskPlannerServiceImpl) computeInstances(
	ctx context.Context,
	rule *model.CollectorTaskRules,
	dist planner.TaskPlanner,
	oldAssignment map[string]string,
	nodeTaskCount map[string]int,
) ([]*model.CollectorTaskInstance, error) {
	// 1. 获取匹配的节点（已按 space/biz/data_type/在线 过滤）
	nodes, err := dist.GetMatchingNodes(ctx, rule)
	if err != nil {
		return nil, fmt.Errorf("failed to get matching nodes: %w", err)
	}
	if len(nodes) == 0 {
		log.InfoContextf(ctx, "[TaskPlanner] No matching nodes for rule %s (spaceID=%s)",
			rule.RuleID, rule.SpaceID)
		return nil, nil
	}

	// D1: 候选节点按 node_id 字典序稳定排序，保证后续分配可复现
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].NodeID < nodes[j].NodeID
	})

	// 2. 获取目标对象（交易标的）
	objects, err := dist.GetTargetObjects(ctx, rule)
	if err != nil {
		return nil, fmt.Errorf("failed to get target objects: %w", err)
	}
	// 如果没有对象，生成一个 symbol="" 的实例（统一处理）
	if len(objects) == 0 {
		objects = []string{""}
	}

	// D1: 对象按字典序稳定排序，消除 GetTargetObjects 返回顺序的不确定性
	sort.Strings(objects)

	log.InfoContextf(ctx, "[TaskPlanner] Distributing %d objects to %d nodes for rule %s (spaceID=%s)",
		len(objects), len(nodes), rule.RuleID, rule.SpaceID)

	// 3. 为每个对象创建实例
	var instances []*model.CollectorTaskInstance
	for _, object := range objects {
		instance, err := s.createInstanceForObject(ctx, rule, object, dist, nodes, oldAssignment, nodeTaskCount)
		if err != nil {
			log.WarnContextf(ctx, "[TaskPlanner] Failed to create instance for %s (ruleID=%s): %v",
				object, rule.RuleID, err)
			continue
		}
		instances = append(instances, instance)
	}

	return instances, nil
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

	// 获取所有启用的规则（规划器全量重算：spaceID 传空，加载所有空间规则后再按 space 分组）
	rules, err := s.taskRulesDAO.GetTaskRulesList(ctx, "", "", "", "", model.EnabledTrue)
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
// 整合 B5（按 space 维度分组重算）、D1（规则/对象/节点稳定排序）：
//   - 规则按 (space_id, rule_id) 排序后遍历，消除 map 遍历顺序不确定性
//   - nodeTaskCount 按 space 独立计数（不同 space 节点不共享）
//   - oldAssignment 用于 D2 优先保留旧 planned_exec_node
//
// 以规则为驱动遍历，避免因节点 SupportedCollectors 未包含某数据类型
// 而导致对应规则被整体跳过（如 symbol 类型规则）
func (s *TaskPlannerServiceImpl) computeAllInstances(
	ctx context.Context,
	_ map[string]bool,
	_ map[string][]string,
	rulesByDataType map[string][]*model.CollectorTaskRules,
	oldAssignment map[string]string,
) ([]*model.CollectorTaskInstance, int, int, error) {
	log.InfoContext(ctx, "[TaskPlanner] Step 4: Computing all instances with stable assignment...")

	var allNewInstances []*model.CollectorTaskInstance
	var syncedRules, failedRules int

	// D1: 数据类型按字典序排序，消除 map 遍历顺序不确定性
	dataTypes := make([]string, 0, len(rulesByDataType))
	for dataType := range rulesByDataType {
		dataTypes = append(dataTypes, dataType)
	}
	sort.Strings(dataTypes)

	for _, dataType := range dataTypes {
		typeRules := rulesByDataType[dataType]
		if len(typeRules) == 0 {
			continue
		}

		// D1: 规则按 (space_id, rule_id) 稳定排序
		sort.Slice(typeRules, func(i, j int) bool {
			if typeRules[i].SpaceID != typeRules[j].SpaceID {
				return typeRules[i].SpaceID < typeRules[j].SpaceID
			}
			return typeRules[i].RuleID < typeRules[j].RuleID
		})

		// 获取该数据类型的分配器
		dist := s.getDistributor(ctx, dataType)
		if dist == nil {
			log.WarnContextf(ctx, "[TaskPlanner] No distributor for data type: %s", dataType)
			failedRules += len(typeRules)
			continue
		}

		log.InfoContextf(ctx, "[TaskPlanner] Processing data type '%s': %d rules", dataType, len(typeRules))

		// B5: nodeTaskCount 按 space 独立计数，避免跨 space 节点共享负载统计
		//     同一 space 内不同规则共享该 space 的计数
		spaceTaskCount := make(map[string]map[string]int)

		// 处理该数据类型的所有规则
		for _, ruleModel := range typeRules {
			rule := ruleModel

			nodeTaskCount, ok := spaceTaskCount[rule.SpaceID]
			if !ok {
				nodeTaskCount = make(map[string]int)
				spaceTaskCount[rule.SpaceID] = nodeTaskCount
			}

			computedInstances, err := s.computeInstances(ctx, rule, dist, oldAssignment, nodeTaskCount)
			if err != nil {
				log.ErrorContextf(ctx, "[TaskPlanner] Failed to compute instances for rule %s: %v",
					rule.RuleID, err)
				failedRules++
				continue
			}

			allNewInstances = append(allNewInstances, computedInstances...)
			log.InfoContextf(ctx, "[TaskPlanner] Rule %s computed: %d instances (spaceID=%s)",
				rule.RuleID, len(computedInstances), rule.SpaceID)
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

// RecalculateAllTaskInstances 重算所有启用规则的任务实例
// 流程：
// 1. 加载在线节点（仅Online状态）
// 2. 提取支持的数据类型
// 3. 加载启用的规则
// 4. 计算所有任务实例（稳定分配：保留旧节点优先 + Rendezvous Hash）
// 5. 全量覆盖写入内存（原子操作）
// 6. 异步刷新到数据库（空列表也清空）
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

	// 步骤3.5：构建旧分配快照（D2：优先保留旧 planned_exec_node）
	// 必须在 ReplaceAll 之前取快照，否则旧数据已被清空
	oldAssignment := s.buildOldAssignment(ctx)

	// 步骤4：计算所有任务实例（稳定分配）
	allNewInstances, syncedCount, failedCount, err := s.computeAllInstances(
		ctx, dataTypeSet, nodesByDataType, rulesByDataType, oldAssignment)
	if err != nil {
		return err
	}

	// 步骤5：覆盖写入内存
	s.memStore.ReplaceAll(ctx, allNewInstances)

	// 步骤6：刷DB（空列表也清空，保证内存/DB 一致）
	s.flushToDB(ctx, allNewInstances)

	elapsed := time.Since(startTime)
	log.InfoContextf(ctx, "[TaskPlanner] Recalculation completed in %v: total_rules=%d, "+
		"synced=%d, failed=%d, instances=%d, version=%d",
		elapsed, len(rules), syncedCount, failedCount, len(allNewInstances), s.memStore.GetVersion())

	return nil
}

// buildOldAssignment 构建旧分配快照：taskID -> 旧 planned_exec_node
// 用于 D2「优先保留旧分配」与 D4「节点变化才迁移」
// 必须在 ReplaceAll 之前调用
func (s *TaskPlannerServiceImpl) buildOldAssignment(_ context.Context) map[string]string {
	snapshot := s.memStore.GetSnapshot()
	if len(snapshot) == 0 {
		return map[string]string{}
	}
	old := make(map[string]string, len(snapshot))
	for _, inst := range snapshot {
		if inst == nil || inst.TaskID == "" {
			continue
		}
		old[inst.TaskID] = inst.PlannedExecNode
	}
	return old
}

// flushToDB 异步刷库
// 将内存中的任务实例全量替换到数据库（Truncate + BatchCreate）
// 注意：空列表也要执行清空，否则 DB 会残留旧实例导致内存与 DB 不一致
func (s *TaskPlannerServiceImpl) flushToDB(ctx context.Context, instances []*model.CollectorTaskInstance) {
	startTime := time.Now()
	log.InfoContextf(ctx, "[TaskPlanner] Starting async DB flush: %d instances", len(instances))

	s.logDuplicateTaskIDs(ctx, instances)

	// 使用 TruncateAndBatchCreate 全量替换
	// 即使 instances 为空，也要执行 Truncate 清空 DB 旧数据
	// （TruncateAndBatchCreate 内部对空切片只做 DELETE 不做插入，已支持）
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
