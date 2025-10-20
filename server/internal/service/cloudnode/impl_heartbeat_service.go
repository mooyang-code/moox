package cloudnode

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"

	"trpc.group/trpc-go/trpc-go/log"
)

// ========== 心跳上报 ==========

// ReportHeartbeat 上报心跳
func (s *ServiceImpl) ReportHeartbeat(ctx context.Context, req *types.ReportHeartbeatRequest) error {
	if req == nil {
		return fmt.Errorf("request is nil")
	}

	if req.NodeID == "" || req.NodeType == "" {
		return fmt.Errorf("node_id and node_type are required")
	}

	return s.handleHeartbeat(ctx, req)
}

// BatchReportHeartbeat 批量上报心跳
func (s *ServiceImpl) BatchReportHeartbeat(ctx context.Context, req *types.BatchReportHeartbeatRequest) error {
	if req == nil {
		return fmt.Errorf("request is nil")
	}

	if len(req.Heartbeats) == 0 {
		return fmt.Errorf("heartbeats list is empty")
	}

	return s.handleBatchHeartbeat(ctx, req)
}

// handleHeartbeat 处理心跳上报
func (s *ServiceImpl) handleHeartbeat(ctx context.Context, req *types.ReportHeartbeatRequest) error {
	// 1. 查询或创建节点记录
	record, err := s.heartbeatDAO.GetByNode(ctx, req.NodeID, req.NodeType)
	if err != nil {
		return fmt.Errorf("query node record failed: %w", err)
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

	// 更新状态为在线
	record.Status = types.NodeStatusOnline

	// 更新源服务
	if req.SourceService != "" {
		record.SourceService = req.SourceService
	}

	// 更新元数据
	if req.Metadata != nil {
		record.Metadata = req.Metadata
	}

	// 4. 保存到数据库
	if isNew {
		if err := s.heartbeatDAO.Create(ctx, record); err != nil {
			return fmt.Errorf("create heartbeat record failed: %w", err)
		}
	} else {
		if err := s.heartbeatDAO.Update(ctx, record); err != nil {
			return fmt.Errorf("update heartbeat record failed: %w", err)
		}
	}
	return nil
}

// handleBatchHeartbeat 处理批量心跳上报
func (s *ServiceImpl) handleBatchHeartbeat(ctx context.Context, req *types.BatchReportHeartbeatRequest) error {
	if len(req.Heartbeats) == 0 {
		return nil
	}

	// 串行处理批量心跳，避免并发问题
	for _, heartbeat := range req.Heartbeats {
		if err := s.handleHeartbeat(ctx, &heartbeat); err != nil {
			// 记录错误但继续处理其他心跳
			log.ErrorContextf(ctx, "[heartbeat] handle heartbeat failed for node %s:%s, error: %v",
				heartbeat.NodeID, heartbeat.NodeType, err)
		}
	}

	return nil
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
		Status:              types.NodeStatusOnline,
		FirstHeartbeat:      &now,
		LastHeartbeat:       &now,
		HeartbeatInterval:   10, // 默认10秒
		TimeoutThreshold:    30, // 默认30秒超时
		ConsecutiveTimeouts: 0,
		TotalTimeouts:       0,
		TotalHeartbeats:     1,
		Metadata:            req.Metadata,
		ProbeEnabled:        true,
		LastProbeResult:     false,
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
	existing, err := s.heartbeatDAO.GetByNode(ctx, req.NodeID, req.NodeType)
	if err != nil {
		return nil, fmt.Errorf("check existing node failed: %w", err)
	}

	if existing != nil {
		return nil, fmt.Errorf("node %s:%s already exists", req.NodeID, req.NodeType)
	}

	// 创建新节点记录
	record := &types.HeartbeatNode{
		NodeID:            req.NodeID,
		NodeType:          req.NodeType,
		SourceService:     req.SourceService,
		Status:            types.NodeStatusOffline, // 初始状态为离线
		HeartbeatInterval: req.HeartbeatInterval,
		TimeoutThreshold:  req.TimeoutThreshold,
		ProbeEnabled:      req.ProbeEnabled,
		ProbeURL:          req.ProbeURL,
		Metadata:          req.Metadata,
	}

	// 设置默认值
	if record.HeartbeatInterval <= 0 {
		record.HeartbeatInterval = 10
	}
	if record.TimeoutThreshold <= 0 {
		record.TimeoutThreshold = 30
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
	record, err := s.heartbeatDAO.GetByNode(ctx, nodeID, nodeType)
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
	record, err := s.heartbeatDAO.GetByNode(ctx, nodeID, nodeType)
	if err != nil {
		return nil, fmt.Errorf("get node record failed: %w", err)
	}

	return record, nil
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
	record, err := s.heartbeatDAO.GetByNode(ctx, req.NodeID, req.NodeType)
	if err != nil {
		return fmt.Errorf("get node record failed: %w", err)
	}

	if record == nil {
		return fmt.Errorf("node %s:%s not found", req.NodeID, req.NodeType)
	}

	// 更新配置
	if req.HeartbeatInterval != nil {
		record.HeartbeatInterval = *req.HeartbeatInterval
	}
	if req.TimeoutThreshold != nil {
		record.TimeoutThreshold = *req.TimeoutThreshold
	}
	if req.ProbeEnabled != nil {
		record.ProbeEnabled = *req.ProbeEnabled
	}
	if req.ProbeURL != nil {
		record.ProbeURL = *req.ProbeURL
	}

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

	return s.heartbeatProber.ProbeHeartbeatNode(ctx, record, action)
}

// ========== 服务控制 ==========

// StartHeartbeatService 启动心跳服务
func (s *ServiceImpl) StartHeartbeatService(ctx context.Context) error {
	// 启动探测器（prober 内部会检查是否已经运行）
	if err := s.heartbeatProber.Start(); err != nil {
		return fmt.Errorf("start prober failed: %w", err)
	}

	return nil
}

// StopHeartbeatService 停止心跳服务
func (s *ServiceImpl) StopHeartbeatService(ctx context.Context) error {
	// 停止探测器（prober 内部会检查是否已经运行）
	if err := s.heartbeatProber.Stop(); err != nil {
		log.ErrorContextf(ctx, "[heartbeat] stop prober failed: %v", err)
	}

	return nil
}
