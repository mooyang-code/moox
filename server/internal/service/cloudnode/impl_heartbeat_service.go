package cloudnode

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"

	"trpc.group/trpc-go/trpc-database/localcache"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	supportedCollectorsCacheTTLSeconds int64 = 50
	packageVersionCacheTTLSeconds      int64 = 30
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
	// 将心跳写入队列（合并并批量写入）
	if s.heartbeatQueue == nil {
		return nil, fmt.Errorf("heartbeat queue not initialized")
	}
	if err := s.heartbeatQueue.Enqueue(ctx, req); err != nil {
		return nil, fmt.Errorf("enqueue heartbeat update failed: %w", err)
	}

	if err := s.updateSupportedCollectors(ctx, req); err != nil {
		return nil, err
	}

	packageVersion := s.loadPackageVersionForHeartbeat(ctx, req.NodeID)

	// 返回包含版本信息的响应（用于云节点自检自己的版本是否一致，若不一致自己会挂掉，因为云节点可能同时存在多个版本实例）
	response := &types.ReportHeartbeatResponse{
		PackageVersion: packageVersion,
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

// createNewRecord 创建新的心跳记录
func (s *ServiceImpl) createNewRecord(req *types.ReportHeartbeatRequest) *types.HeartbeatNode {
	now := time.Now()
	if req.Timestamp != nil {
		now = *req.Timestamp
	}

	record := &types.HeartbeatNode{
		NodeID:              req.NodeID,
		NodeType:            req.NodeType,
		SourceService:       req.SourceService,
		LastHeartbeat:       &now,
		ConsecutiveTimeouts: 0,
		TotalTimeouts:       0,
		TotalHeartbeats:     1,
		Metadata:            req.Metadata,
		LastProbeResult:     "",
	}
	return record
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

// ========== 节点管理 ==========

// RegisterHeartbeatNode 注册心跳节点
func (s *ServiceImpl) RegisterHeartbeatNode(ctx context.Context, req *types.RegisterNodeRequest) (*types.HeartbeatNode, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}

	if req.NodeID == "" || req.NodeType == "" {
		return nil, fmt.Errorf("node_id and node_type are required")
	}

	// 检查节点是否已存在
	existing, err := s.heartbeatDAO.GetNodeByID(ctx, req.NodeID)
	if err != nil {
		return nil, fmt.Errorf("check existing node failed: %w", err)
	}

	if existing != nil {
		return nil, fmt.Errorf("node %s:%s already exists", req.NodeID, req.NodeType)
	}

	// 创建新节点记录（移除已删除的字段）
	record := &types.HeartbeatNode{
		NodeID:        req.NodeID,
		NodeType:      req.NodeType,
		SourceService: req.SourceService,
		Metadata:      req.Metadata,
	}

	// 保存到数据库
	if err := s.heartbeatDAO.Create(ctx, record); err != nil {
		return nil, fmt.Errorf("create node record failed: %w", err)
	}
	return record, nil
}

// UnregisterHeartbeatNode 注销心跳节点
func (s *ServiceImpl) UnregisterHeartbeatNode(ctx context.Context, nodeID, nodeType string) error {
	if nodeID == "" || nodeType == "" {
		return fmt.Errorf("node_id and node_type are required")
	}

	// 查找节点记录
	record, err := s.heartbeatDAO.GetNodeByID(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get node record failed: %w", err)
	}

	if record == nil {
		return fmt.Errorf("node %s:%s not found", nodeID, nodeType)
	}

	// 软删除节点记录
	if err := s.heartbeatDAO.Delete(ctx, record.ID); err != nil {
		return fmt.Errorf("delete node record failed: %w", err)
	}
	return nil
}

// GetHeartbeatNode 获取节点心跳信息
func (s *ServiceImpl) GetHeartbeatNode(ctx context.Context, nodeID, nodeType string) (*types.HeartbeatNode, error) {
	if nodeID == "" || nodeType == "" {
		return nil, fmt.Errorf("node_id and node_type are required")
	}

	// 直接从数据库获取
	record, err := s.heartbeatDAO.GetNodeByID(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get node record failed: %w", err)
	}

	return record, nil
}

// GetNodeStatus 获取节点状态
func (s *ServiceImpl) GetNodeStatus(ctx context.Context, nodeID string) (*types.NodeStatus, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("node_id is required")
	}

	return s.heartbeatDAO.GetNodeStatus(ctx, nodeID)
}

// ListHeartbeatNodes 列出心跳节点
func (s *ServiceImpl) ListHeartbeatNodes(ctx context.Context, filter *types.NodeFilter) ([]*types.HeartbeatNode, int64, error) {
	return s.heartbeatDAO.List(ctx, filter)
}

// UpdateHeartbeatNodeConfig 更新心跳节点配置
func (s *ServiceImpl) UpdateHeartbeatNodeConfig(ctx context.Context, req *types.UpdateNodeConfigRequest) error {
	if req == nil {
		return fmt.Errorf("request is nil")
	}

	if req.NodeID == "" || req.NodeType == "" {
		return fmt.Errorf("node_id and node_type are required")
	}

	// 获取节点记录
	record, err := s.heartbeatDAO.GetNodeByID(ctx, req.NodeID)
	if err != nil {
		return fmt.Errorf("get node record failed: %w", err)
	}

	if record == nil {
		return fmt.Errorf("node %s:%s not found", req.NodeID, req.NodeType)
	}

	// 更新配置（移除已删除的字段）
	// Note: HeartbeatInterval, TimeoutThreshold, ProbeEnabled, ProbeURL 字段已被移除

	// 保存更新
	if err := s.heartbeatDAO.Update(ctx, record); err != nil {
		return fmt.Errorf("update node config failed: %w", err)
	}
	return nil
}

// ========== 探测管理 ==========

// ProbeHeartbeatNode 手动探测心跳节点
func (s *ServiceImpl) ProbeHeartbeatNode(ctx context.Context, nodeID, nodeType, action string) (*types.ProbeResult, error) {
	if nodeID == "" || nodeType == "" {
		return nil, fmt.Errorf("node_id and node_type are required")
	}
	if action == "" {
		action = "health" // 默认健康检查动作
	}

	// 获取节点记录
	record, err := s.GetHeartbeatNode(ctx, nodeID, nodeType)
	if err != nil {
		return nil, fmt.Errorf("get node record failed: %w", err)
	}

	if record == nil {
		return nil, fmt.Errorf("node %s:%s not found", nodeID, nodeType)
	}

	return globalProberInstance.ProbeHeartbeatNode(ctx, record, action)
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
