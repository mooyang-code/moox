package cloudnode

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"
	"github.com/mooyang-code/moox/server/internal/service/dnsproxy"

	"trpc.group/trpc-go/trpc-database/localcache"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	supportedCollectorsCacheTTLSeconds int64 = 50
	packageVersionCacheTTLSeconds      int64 = 30
	nodeTasksCacheTTLSeconds           int64 = 60
)

// ========== 接收心跳上报请求 ==========

// ReportHeartbeat 客户端上报心跳
func (s *ServiceImpl) ReportHeartbeat(ctx context.Context, req *types.ReportHeartbeatRequest) (*types.ReportHeartbeatResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}

	if req.NodeID == "" || req.NodeType == "" {
		return nil, fmt.Errorf("node_id and node_type are required")
	}
	return s.handleHeartbeat(ctx, req)
}

// handleHeartbeat 处理心跳上报（新版：从内存读取任务）
func (s *ServiceImpl) handleHeartbeat(ctx context.Context, req *types.ReportHeartbeatRequest) (*types.ReportHeartbeatResponse, error) {
	log.DebugContextf(ctx, "handleHeartbeat Enter:%s", req.NodeID)

	// 1. 写入内存存储（无锁，高性能）
	if s.heartbeatStore == nil {
		return nil, fmt.Errorf("heartbeat store not initialized")
	}
	s.heartbeatStore.UpdateHeartbeat(req)

	// 2. 处理节点DNS记录（仅更新缓存，不触发探测）
	if err := s.handleNodeDNSRecords(ctx, req.NodeID, req.LocalDNSRecords); err != nil {
		log.ErrorContextf(ctx, "[Heartbeat] Failed to update node DNS records: nodeID=%s, error=%v",
			req.NodeID, err)
	}

	// 3. 更新节点支持的采集器类型
	if err := s.updateSupportedCollectors(ctx, req); err != nil {
		return nil, err
	}

	// 4. 获取包版本信息
	packageVersion := s.loadPackageVersionForHeartbeat(ctx, req.NodeID)

	// 5. 检查任务实例仓库是否初始化
	if s.taskInstanceStore == nil {
		log.WarnContext(ctx, "[Heartbeat] Task instance store not initialized")
		return &types.ReportHeartbeatResponse{
			PackageVersion: packageVersion,
			TasksMD5:       "initializing",
			TaskInstances:  nil,
		}, nil
	}

	// 6. 从内存仓库获取节点任务列表
	tasks := s.loadNodeTasksFromMemory(ctx, req.NodeID)

	// 7. 检查任务仓库是否为空（启动期间）
	storeCount := s.taskInstanceStore.GetCount()
	if storeCount == 0 {
		// 任务仓库为空，返回特殊标记，客户端保持本地任务不变
		log.InfoContextf(ctx, "[Heartbeat] Task instance store is empty, returning initializing flag: nodeID=%s", req.NodeID)
		return &types.ReportHeartbeatResponse{
			PackageVersion: packageVersion,
			TasksMD5:       "initializing", // 特殊标记
			TaskInstances:  nil,
		}, nil
	}

	// 8. 计算服务端任务MD5（仅用于返回，不再参与下发判断）
	serverTasksMD5 := s.calculateTasksMD5(tasks)

	// 9. 构建响应（始终返回任务列表）
	response := &types.ReportHeartbeatResponse{
		PackageVersion: packageVersion,
		TasksMD5:       serverTasksMD5,
		TaskInstances:  tasks,
	}

	return response, nil
}

func (s *ServiceImpl) updateSupportedCollectors(ctx context.Context, req *types.ReportHeartbeatRequest) error {
	if len(req.SupportedCollectors) == 0 {
		return nil
	}

	log.DebugContextf(ctx, "[Heartbeat] 节点 %s 上报采集器类型: %v", req.NodeID, req.SupportedCollectors)
	normalizedCollectors := normalizeCollectors(req.SupportedCollectors)
	if len(normalizedCollectors) == 0 {
		return nil
	}

	cacheKey := supportedCollectorsCacheKey(req.NodeID)
	cached, err := localcache.GetWithLoad(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, error) {
		return s.loadSupportedCollectors(ctx, req.NodeID)
	}, supportedCollectorsCacheTTLSeconds)
	if err != nil {
		log.ErrorContextf(ctx, "[Heartbeat] 加载节点采集器类型失败: nodeID=%s, error=%v", req.NodeID, err)
		return fmt.Errorf("load supported collectors failed: %w", err)
	}

	cachedCollectors, ok := cached.([]string)
	if !ok || !equalCollectors(cachedCollectors, normalizedCollectors) {
		if err := s.nodeDAO.UpdateSupportedCollectors(ctx, req.NodeID, normalizedCollectors); err != nil {
			log.ErrorContextf(ctx, "[Heartbeat] 更新节点采集器类型失败: nodeID=%s, error=%v", req.NodeID, err)
			return fmt.Errorf("update supported collectors failed: %w", err)
		}
		localcache.Set(cacheKey, normalizedCollectors, supportedCollectorsCacheTTLSeconds)
	}

	return nil
}

func (s *ServiceImpl) loadPackageVersionForHeartbeat(ctx context.Context, nodeID string) string {
	packageVersion, err := s.getLatestPackageVersionCached(ctx, nodeID)
	if err != nil {
		log.WarnContextf(ctx, "获取包版本信息失败: %v", err)
		return ""
	}
	return packageVersion
}

func (s *ServiceImpl) loadSupportedCollectors(ctx context.Context, nodeID string) ([]string, error) {
	node, err := s.nodeDAO.GetCloudNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, fmt.Errorf("node %s not found", nodeID)
	}

	currentCollectors, err := parseCollectorsJSON(node.SupportedCollectors)
	if err != nil {
		return nil, nil
	}

	return normalizeCollectors(currentCollectors), nil
}

func (s *ServiceImpl) getLatestPackageVersionCached(ctx context.Context, nodeID string) (string, error) {
	cacheKey := packageVersionCacheKey(nodeID)
	cached, err := localcache.GetWithLoad(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, error) {
		return s.getLatestPackageVersion(ctx, nodeID)
	}, packageVersionCacheTTLSeconds)
	if err != nil {
		return "", err
	}

	if cached == nil {
		return "", nil
	}
	version, ok := cached.(string)
	if ok {
		return version, nil
	}

	localcache.Del(cacheKey)
	version, err = s.getLatestPackageVersion(ctx, nodeID)
	if err != nil {
		return "", err
	}
	localcache.Set(cacheKey, version, packageVersionCacheTTLSeconds)
	return version, nil
}

func parseCollectorsJSON(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var collectors []string
	if err := json.Unmarshal([]byte(raw), &collectors); err != nil {
		return nil, err
	}
	return collectors, nil
}

func normalizeCollectors(collectors []string) []string {
	if len(collectors) == 0 {
		return nil
	}

	set := make(map[string]struct{}, len(collectors))
	for _, value := range collectors {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}

	normalized := make([]string, 0, len(set))
	for value := range set {
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized
}

func equalCollectors(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func supportedCollectorsCacheKey(nodeID string) string {
	return "heartbeat:supported_collectors:" + nodeID
}

func packageVersionCacheKey(nodeID string) string {
	return "heartbeat:package_version:" + nodeID
}

func nodeTasksCacheKey(nodeID string) string {
	return "heartbeat:node_tasks:" + nodeID
}

// GetNodeStatus 获取节点状态
func (s *ServiceImpl) GetNodeStatus(ctx context.Context, nodeID string) (*types.NodeStatus, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("node_id is required")
	}

	// 获取节点的超时阈值配置
	timeoutThreshold := 0
	node, err := s.nodeDAO.GetCloudNode(ctx, nodeID)
	if err == nil && node != nil {
		timeoutThreshold = node.TimeoutThreshold
	}

	status := s.heartbeatStore.GetNodeStatus(nodeID, timeoutThreshold)
	return &status, nil
}

// ========== 版本信息查询 ==========

// getLatestPackageVersion 获取节点最新包版本信息
func (s *ServiceImpl) getLatestPackageVersion(ctx context.Context, nodeID string) (string, error) {
	// 从 nodeDAO 中获取包ID
	node, err := s.nodeDAO.GetCloudNode(ctx, nodeID)
	if err != nil {
		log.ErrorContextf(ctx, "获取节点信息失败: %v", err)
		return "", err
	}
	if node == nil || node.PackageID == "" {
		// 节点不存在或没有包ID，返回空版本
		return "", nil
	}

	// 从 packageDAO 中获取包版本信息
	pkg, err := s.packageDAO.GetByID(ctx, node.PackageID)
	if err != nil {
		log.ErrorContextf(ctx, "获取包信息失败: %v", err)
		return "", err
	}
	if pkg == nil {
		return "", nil
	}
	return pkg.Version, nil
}

// ========== 任务实例查询和MD5计算 ==========

// getNodeTasksCached 获取节点任务列表（带缓存）
func (s *ServiceImpl) getNodeTasksCached(ctx context.Context, nodeID string) ([]*types.TaskInstanceInfo, error) {
	cacheKey := nodeTasksCacheKey(nodeID)
	cached, err := localcache.GetWithLoad(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, error) {
		return s.loadNodeTasks(ctx, nodeID)
	}, nodeTasksCacheTTLSeconds)
	if err != nil {
		return nil, err
	}

	if cached == nil {
		return nil, nil
	}

	tasks, ok := cached.([]*types.TaskInstanceInfo)
	if ok {
		return tasks, nil
	}

	// 缓存类型不匹配，清除缓存并重新加载
	localcache.Del(cacheKey)
	tasks, err = s.loadNodeTasks(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	localcache.Set(cacheKey, tasks, nodeTasksCacheTTLSeconds)
	return tasks, nil
}

// loadNodeTasksFromMemory 从内存仓库加载节点任务（新版）
func (s *ServiceImpl) loadNodeTasksFromMemory(ctx context.Context, nodeID string) []*types.TaskInstanceInfo {
	// 从内存仓库获取该节点的任务实例
	instances := s.taskInstanceStore.GetByNodeID(nodeID)

	// #region agent log
	if len(instances) > 0 {
		symbolsForLog := make([]string, 0, len(instances))
		for _, inst := range instances {
			symbolsForLog = append(symbolsForLog, inst.Symbol)
		}
		log.InfoContextf(ctx, "[DEBUG_AGENT] loadNodeTasksFromMemory: nodeID=%s, taskCount=%d, symbols=%v",
			nodeID, len(instances), symbolsForLog)

		// 详细日志：输出每个任务的 TaskParams
		for i, inst := range instances {
			if i < 3 { // 只输出前3个任务
				log.InfoContextf(ctx, "[DEBUG_AGENT] Task[%d]: taskID=%s, symbol=%s, taskParams=%s",
					i, inst.TaskID, inst.Symbol, inst.TaskParams)
			}
		}
	}
	// #endregion

	// 转换为 TaskInstanceInfo
	tasks := make([]*types.TaskInstanceInfo, 0, len(instances))
	for _, inst := range instances {
		tasks = append(tasks, &types.TaskInstanceInfo{
			ID:              inst.ID,
			TaskID:          inst.TaskID,
			RuleID:          inst.RuleID,
			PlannedExecNode: inst.PlannedExecNode,
			DataType:        inst.DataType,
			Symbol:          inst.Symbol,
			Interval:        inst.Interval,
			TaskParams:      inst.TaskParams,
			Invalid:         inst.Invalid,
		})
	}

	return tasks
}

// loadNodeTasks 从DB加载节点任务（旧版，保留用于兼容）
func (s *ServiceImpl) loadNodeTasks(ctx context.Context, nodeID string) ([]*types.TaskInstanceInfo, error) {
	// 获取节点的所有任务实例（不限制状态）
	instances, err := s.taskInstanceDAO.GetTaskInstancesByNode(ctx, nodeID, nil)
	if err != nil {
		log.ErrorContextf(ctx, "[Heartbeat] 加载节点任务失败: nodeID=%s, error=%v", nodeID, err)
		return nil, fmt.Errorf("load node tasks failed: %w", err)
	}

	// 转换为 TaskInstanceInfo，过滤掉 Invalid 的任务
	var tasks []*types.TaskInstanceInfo
	for _, instance := range instances {
		// 跳过已标记为Invalid的任务
		if instance.Invalid != 0 {
			continue
		}

		tasks = append(tasks, &types.TaskInstanceInfo{
			ID:              instance.ID,
			TaskID:          instance.TaskID,
			RuleID:          instance.RuleID,
			PlannedExecNode: instance.PlannedExecNode,
			DataType:        instance.CollectDataType,
			Symbol:          instance.Symbol,
			Interval:        instance.Interval,
			TaskParams:      instance.TaskParams,
			Invalid:         instance.Invalid,
		})
	}

	return tasks, nil
}

// calculateTasksMD5 计算任务MD5值
func (s *ServiceImpl) calculateTasksMD5(tasks []*types.TaskInstanceInfo) string {
	if len(tasks) == 0 {
		return "empty"
	}

	// 提取所有TaskID并排序
	taskIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.TaskID)
	}
	sort.Strings(taskIDs)

	// 拼接成字符串
	combined := strings.Join(taskIDs, ",")

	// 计算MD5
	hash := md5.Sum([]byte(combined))
	return hex.EncodeToString(hash[:])
}

// handleNodeDNSRecords 处理节点DNS记录上报
// 仅将DNS记录更新到缓存，不触发探测（探测由独立定时器完成）
func (s *ServiceImpl) handleNodeDNSRecords(ctx context.Context, nodeID string, records []*types.LocalDNSReportItem) error {
	// 允许空记录（客户端解析失败场景）
	if len(records) == 0 {
		log.InfoContextf(ctx, "[Heartbeat] Node reported empty DNS records: nodeID=%s", nodeID)
		return nil
	}

	// 导入dnsproxy包（需要在文件头部添加import）
	// 转换为dnsproxy的NodeDNSRecord格式
	dnsRecords := make([]*dnsproxy.NodeDNSRecord, 0, len(records))
	for _, item := range records {
		dnsRecords = append(dnsRecords, &dnsproxy.NodeDNSRecord{
			Domain:    item.Domain,
			IPList:    item.IPList,
			ResolveAt: item.ResolveAt,
		})
	}

	// 更新到缓存（365天TTL）
	if err := dnsproxy.UpdateNodeDNSRecords(ctx, nodeID, dnsRecords); err != nil {
		return fmt.Errorf("failed to update node DNS records: %w", err)
	}

	log.InfoContextf(ctx, "[Heartbeat] Node DNS records updated: nodeID=%s, domains=%d",
		nodeID, len(records))

	return nil
}

