package dao

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NodeHeartbeatDAO 节点心跳DAO接口
type NodeHeartbeatDAO interface {
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

// nodeHeartbeatDAOImpl 节点心跳DAO实现
type nodeHeartbeatDAOImpl struct {
	db *gorm.DB
}

// NewNodeHeartbeatDAO 创建节点心跳DAO
func NewNodeHeartbeatDAO(db *gorm.DB) NodeHeartbeatDAO {
	return &nodeHeartbeatDAOImpl{db: db}
}

// UpdateHeartbeat 更新或创建心跳记录
func (d *nodeHeartbeatDAOImpl) UpdateHeartbeat(ctx context.Context, heartbeat *model.NodeHeartbeat) error {
	if heartbeat.NodeID == "" {
		return fmt.Errorf("node_id is required")
	}

	// 使用 UPSERT 操作
	err := d.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_last_heartbeat",
			"c_status", "c_metrics", "c_mtime",
		}),
	}).Create(heartbeat).Error

	return err
}

// GetNodeHeartbeat 获取节点心跳记录
func (d *nodeHeartbeatDAOImpl) GetNodeHeartbeat(ctx context.Context, nodeID string) (*model.NodeHeartbeat, error) {
	var heartbeat model.NodeHeartbeat
	err := d.db.WithContext(ctx).
		Where("c_node_id = ?", nodeID).
		First(&heartbeat).Error

	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}

	return &heartbeat, err
}

// UpdateNodeStatus 更新节点状态
func (d *nodeHeartbeatDAOImpl) UpdateNodeStatus(ctx context.Context, nodeID string, status int) error {
	return d.db.WithContext(ctx).
		Model(&model.NodeHeartbeat{}).
		Where("c_node_id = ?", nodeID).
		Updates(map[string]interface{}{
			"c_status": status,
			"c_mtime":  time.Now(),
		}).Error
}

// GetOfflineNodes 获取离线节点列表
func (d *nodeHeartbeatDAOImpl) GetOfflineNodes(ctx context.Context, offlineDuration time.Duration) ([]*model.NodeHeartbeat, error) {
	var heartbeats []*model.NodeHeartbeat
	threshold := time.Now().Add(-offlineDuration)

	err := d.db.WithContext(ctx).
		Where("c_last_heartbeat < ? OR c_status = ?", threshold, 0).
		Find(&heartbeats).Error

	return heartbeats, err
}

// DeleteOldHeartbeats 删除旧的心跳记录
func (d *nodeHeartbeatDAOImpl) DeleteOldHeartbeats(ctx context.Context, days int) error {
	threshold := time.Now().AddDate(0, 0, -days)

	return d.db.WithContext(ctx).
		Where("c_mtime < ?", threshold).
		Delete(&model.NodeHeartbeat{}).Error
}
