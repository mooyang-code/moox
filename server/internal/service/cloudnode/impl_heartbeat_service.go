package cloudnode

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"

	"trpc.group/trpc-go/trpc-go/log"
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
	log.InfoContextf(ctx, "handleHeartbeat Enter")
	// 1. 查询或创建节点记录
	record, err := s.heartbeatDAO.GetNodeByID(ctx, req.NodeID)
	if err != nil {
		return nil, fmt.Errorf("query node record failed: %w", err)
	}

	var isNew bool
	if record == nil {
		// 首次心跳，创建新记录
		record = s.createNewRecord(req)
		isNew = true
	}

	// 2. 更新心跳数据
	now := time.Now()
	if req.Timestamp != nil {
		now = *req.Timestamp
	}

	record.LastHeartbeat = &now
	record.TotalHeartbeats++
	record.ConsecutiveTimeouts = 0

	// 如果是新记录，设置首次心跳时间
	if isNew {
		record.FirstHeartbeat = &now
	}

	// 更新源服务
	if req.SourceService != "" {
		record.SourceService = req.SourceService
	}

	// 更新元数据 - 智能合并，避免覆盖现有数据
	if req.Metadata != nil {
		if record.Metadata == nil {
			record.Metadata = make(map[string]interface{})
		}
		// 将请求中的metadata元素合并到现有metadata中
		for key, value := range req.Metadata {
			record.Metadata[key] = value
		}
	}

	// 4. 保存到数据库
	if isNew {
		if err := s.heartbeatDAO.Create(ctx, record); err != nil {
			return nil, fmt.Errorf("create heartbeat record failed: %w", err)
		}
	} else {
		if err := s.heartbeatDAO.Update(ctx, record); err != nil {
			return nil, fmt.Errorf("update heartbeat record failed: %w", err)
		}
	}

	// 5. 获取包版本信息
	packageVersion, err := s.getLatestPackageVersion(ctx, req.NodeID)
	if err != nil {
		log.WarnContextf(ctx, "获取包版本信息失败: %v", err)
		// 版本信息获取失败不影响心跳上报，返回空版本
		packageVersion = ""
	}

	// 6. 返回包含版本信息的响应
	response := &types.ReportHeartbeatResponse{
		PackageVersion: packageVersion,
	}
	return response, nil
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
		FirstHeartbeat:      &now,
		LastHeartbeat:       &now,
		ConsecutiveTimeouts: 0,
		TotalTimeouts:       0,
		TotalHeartbeats:     1,
		Metadata:            req.Metadata,
		LastProbeResult:     "",
	}
	return record
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
