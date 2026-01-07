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

// handleHeartbeat 处理心跳上报
func (s *ServiceImpl) handleHeartbeat(ctx context.Context, req *types.ReportHeartbeatRequest) (*types.ReportHeartbeatResponse, error) {
	log.DebugContextf(ctx, "handleHeartbeat Enter")
	// 直接写入内存存储（无锁，高性能）
	if s.heartbeatStore == nil {
		return nil, fmt.Errorf("heartbeat store not initialized")
	}
	s.heartbeatStore.UpdateHeartbeat(req)

	if err := s.updateSupportedCollectors(ctx, req); err != nil {
		return nil, err
	}

	// 获取包版本信息
	packageVersion := s.loadPackageVersionForHeartbeat(ctx, req.NodeID)

	// 获取节点任务列表（带缓存）
	tasks, err := s.getNodeTasksCached(ctx, req.NodeID)
	if err != nil {
		log.ErrorContextf(ctx, "[Heartbeat] 获取节点任务失败: nodeID=%s, error=%v", req.NodeID, err)
		// 任务查询失败不影响心跳，返回空任务列表
		tasks = nil
	}

	// 计算服务端任务MD5
	serverTasksMD5 := s.calculateTasksMD5(tasks)

	// 构建响应
	response := &types.ReportHeartbeatResponse{
		PackageVersion: packageVersion,
		TasksMD5:       serverTasksMD5,
	}

	// MD5比较：如果客户端上报的MD5与服务端计算的MD5不同，则返回任务列表
	if req.TasksMD5 != serverTasksMD5 {
		log.InfoContextf(ctx, "[Heartbeat] 任务MD5不匹配，返回任务列表: nodeID=%s, clientMD5=%s, serverMD5=%s, taskCount=%d",
			req.NodeID, req.TasksMD5, serverTasksMD5, len(tasks))
		response.TaskInstances = tasks
	} else {
		log.DebugContextf(ctx, "[Heartbeat] 任务MD5匹配，跳过任务下发: nodeID=%s, md5=%s", req.NodeID, serverTasksMD5)
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

// loadNodeTasks 从DB加载节点任务
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
			ID:         instance.ID,
			TaskID:     instance.TaskID,
			RuleID:     instance.RuleID,
			NodeID:     instance.NodeID,
			TaskParams: instance.TaskParams,
			Invalid:    instance.Invalid,
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
