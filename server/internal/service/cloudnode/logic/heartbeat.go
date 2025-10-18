package logic

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"

	"gorm.io/gorm"
)

// HeartbeatService 心跳服务接口
type HeartbeatService interface {
	// UpdateHeartbeat 更新或创建心跳记录
	UpdateHeartbeat(ctx context.Context, heartbeat *model.NodeHeartbeat) error
	// GetNodeHeartbeat 获取节点心跳记录
	GetNodeHeartbeat(ctx context.Context, nodeID string) (*model.NodeHeartbeat, error)
	// UpdateNodeStatus 更新节点状态
	UpdateNodeStatus(ctx context.Context, nodeID string, status int) error
	// GetOfflineNodes 获取离线节点列表
	GetOfflineNodes(ctx context.Context, offlineDuration time.Duration) ([]*model.NodeHeartbeat, error)
	// DeleteOldHeartbeats 删除旧的心跳记录
	DeleteOldHeartbeats(ctx context.Context, days int) error
}

type heartbeatServiceImpl struct {
	heartbeatDAO dao.NodeHeartbeatDAO
}

// NewHeartbeatService 创建心跳服务
func NewHeartbeatService(db *gorm.DB) HeartbeatService {
	return &heartbeatServiceImpl{
		heartbeatDAO: dao.NewNodeHeartbeatDAO(db),
	}
}

// UpdateHeartbeat 更新或创建心跳记录
func (s *heartbeatServiceImpl) UpdateHeartbeat(ctx context.Context, heartbeat *model.NodeHeartbeat) error {
	return s.heartbeatDAO.UpdateHeartbeat(ctx, heartbeat)
}

// GetNodeHeartbeat 获取节点心跳记录
func (s *heartbeatServiceImpl) GetNodeHeartbeat(ctx context.Context, nodeID string) (*model.NodeHeartbeat, error) {
	return s.heartbeatDAO.GetNodeHeartbeat(ctx, nodeID)
}

// UpdateNodeStatus 更新节点状态
func (s *heartbeatServiceImpl) UpdateNodeStatus(ctx context.Context, nodeID string, status int) error {
	return s.heartbeatDAO.UpdateNodeStatus(ctx, nodeID, status)
}

// GetOfflineNodes 获取离线节点列表
func (s *heartbeatServiceImpl) GetOfflineNodes(ctx context.Context, offlineDuration time.Duration) ([]*model.NodeHeartbeat, error) {
	return s.heartbeatDAO.GetOfflineNodes(ctx, offlineDuration)
}

// DeleteOldHeartbeats 删除旧的心跳记录
func (s *heartbeatServiceImpl) DeleteOldHeartbeats(ctx context.Context, days int) error {
	return s.heartbeatDAO.DeleteOldHeartbeats(ctx, days)
}
