package dao

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"gorm.io/gorm"
)

// SCFNodeDAO 节点数据访问对象接口
type SCFNodeDAO interface {
	GetSCFNodeList(ctx context.Context) ([]*model.SCFNode, error)
	GetSCFNode(ctx context.Context, nodeID string) (*model.SCFNode, error)
	GetSCFNodesByType(ctx context.Context, nodeType string) ([]*model.SCFNode, error)
	GetOnlineNodes(ctx context.Context) ([]*model.SCFNode, error)
	GetSCFNodesByRegion(ctx context.Context, region string) ([]*model.SCFNode, error)
	GetNamespaceStats(ctx context.Context, region string) (map[string]int, error)
	CreateSCFNode(ctx context.Context, node *model.SCFNode) error
	UpdateSCFNode(ctx context.Context, node *model.SCFNode) error
	DeleteSCFNode(ctx context.Context, nodeID string) error
	UpdateNodeHeartbeat(ctx context.Context, nodeID string, currentLoad string) error
	UpdateNodeLoad(ctx context.Context, nodeID string, currentLoad string) error
	UpdateNodeMetadata(ctx context.Context, nodeID string, metadata string) error
	UpdateNodeStatus(ctx context.Context, nodeID string, status int) error
}

type scfNodeDaoImpl struct {
	db *gorm.DB
}

// NewSCFNodeDAO 创建新的节点DAO实例
func NewSCFNodeDAO(db *gorm.DB) SCFNodeDAO {
	return &scfNodeDaoImpl{db: db}
}

func (d *scfNodeDaoImpl) GetSCFNodeList(ctx context.Context) ([]*model.SCFNode, error) {
	var nodes []*model.SCFNode
	result := d.db.WithContext(ctx).
		Where("c_invalid = ?", 0).
		Order("c_mtime DESC").
		Find(&nodes)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get collector nodes: %w", result.Error)
	}
	return nodes, nil
}

func (d *scfNodeDaoImpl) GetSCFNode(ctx context.Context, nodeID string) (*model.SCFNode, error) {
	var node model.SCFNode
	result := d.db.WithContext(ctx).
		Where("c_node_id = ? AND c_invalid = ?", nodeID, 0).
		First(&node)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get collector node: %w", result.Error)
	}
	return &node, nil
}

func (d *scfNodeDaoImpl) CreateSCFNode(ctx context.Context, node *model.SCFNode) error {
	node.CreateTime = time.Now()
	node.ModifyTime = time.Now()

	result := d.db.WithContext(ctx).Create(node)
	if result.Error != nil {
		return fmt.Errorf("failed to create collector node: %w", result.Error)
	}
	return nil
}

func (d *scfNodeDaoImpl) UpdateSCFNode(ctx context.Context, node *model.SCFNode) error {
	node.ModifyTime = time.Now()

	result := d.db.WithContext(ctx).
		Model(&model.SCFNode{}).
		Where("c_node_id = ? AND c_invalid = ?", node.NodeID, 0).
		Updates(map[string]interface{}{
			"c_cloud_account_id":    node.CloudAccountID,
			"c_namespace":           node.Namespace,
			"c_node_type":           node.NodeType,
			"c_region":              node.Region,
			"c_ip_address":          node.IPAddress,
			"c_supported_collectors": node.SupportedCollectors,
			"c_capacity":            node.Capacity,
			"c_current_load":        node.CurrentLoad,
			"c_status":              node.Status,
			"c_last_heartbeat":      node.LastHeartbeat,
			"c_metadata":            node.Metadata,
			"c_mtime":               node.ModifyTime,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update collector node: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("node not found or already deleted")
	}
	return nil
}

func (d *scfNodeDaoImpl) DeleteSCFNode(ctx context.Context, nodeID string) error {
	result := d.db.WithContext(ctx).
		Model(&model.SCFNode{}).
		Where("c_node_id = ? AND c_invalid = ?", nodeID, 0).
		Updates(map[string]interface{}{
			"c_invalid": 1,
			"c_mtime":   time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("failed to delete collector node: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("node not found or already deleted")
	}
	return nil
}

func (d *scfNodeDaoImpl) UpdateNodeHeartbeat(ctx context.Context, nodeID string, currentLoad string) error {
	now := time.Now()
	updates := map[string]interface{}{
		"c_last_heartbeat": now,
		"c_status":         model.NodeStatusOnline,
		"c_mtime":          now,
	}
	
	if currentLoad != "" {
		updates["c_current_load"] = currentLoad
	}
	
	result := d.db.WithContext(ctx).
		Model(&model.SCFNode{}).
		Where("c_node_id = ? AND c_invalid = ?", nodeID, 0).
		Updates(updates)

	if result.Error != nil {
		return fmt.Errorf("failed to update node heartbeat: %w", result.Error)
	}
	return nil
}

// GetSCFNodesByType 根据节点类型获取节点列表
func (d *scfNodeDaoImpl) GetSCFNodesByType(ctx context.Context, nodeType string) ([]*model.SCFNode, error) {
	var nodes []*model.SCFNode
	result := d.db.WithContext(ctx).
		Where("c_node_type = ? AND c_invalid = ?", nodeType, 0).
		Order("c_mtime DESC").
		Find(&nodes)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get nodes by type: %w", result.Error)
	}
	return nodes, nil
}

// GetOnlineNodes 获取所有在线节点
func (d *scfNodeDaoImpl) GetOnlineNodes(ctx context.Context) ([]*model.SCFNode, error) {
	var nodes []*model.SCFNode
	result := d.db.WithContext(ctx).
		Where("c_status = ? AND c_invalid = ?", model.NodeStatusOnline, 0).
		Order("c_mtime DESC").
		Find(&nodes)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get online nodes: %w", result.Error)
	}
	return nodes, nil
}

// UpdateNodeLoad 更新节点负载信息
func (d *scfNodeDaoImpl) UpdateNodeLoad(ctx context.Context, nodeID string, currentLoad string) error {
	now := time.Now()
	result := d.db.WithContext(ctx).
		Model(&model.SCFNode{}).
		Where("c_node_id = ? AND c_invalid = ?", nodeID, 0).
		Updates(map[string]interface{}{
			"c_current_load": currentLoad,
			"c_mtime":        now,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update node load: %w", result.Error)
	}
	return nil
}

// GetSCFNodesByRegion 根据地区获取节点列表
func (d *scfNodeDaoImpl) GetSCFNodesByRegion(ctx context.Context, region string) ([]*model.SCFNode, error) {
	var nodes []*model.SCFNode
	result := d.db.WithContext(ctx).
		Where("c_region = ? AND c_invalid = ?", region, 0).
		Order("c_mtime DESC").
		Find(&nodes)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get nodes by region: %w", result.Error)
	}
	return nodes, nil
}

// GetNamespaceStats 获取命名空间使用统计
func (d *scfNodeDaoImpl) GetNamespaceStats(ctx context.Context, region string) (map[string]int, error) {
	type NamespaceCount struct {
		Namespace string
		Count     int
	}

	var stats []NamespaceCount
	
	// 查询每个命名空间的节点数量
	// 注意：这里假设每个节点代表一个云函数
	result := d.db.WithContext(ctx).
		Model(&model.SCFNode{}).
		Select("c_namespace as namespace, COUNT(*) as count").
		Where("c_region = ? AND c_invalid = ?", region, 0).
		Group("c_namespace").
		Scan(&stats)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get namespace stats: %w", result.Error)
	}

	// 转换为map格式
	statsMap := make(map[string]int)
	for _, stat := range stats {
		statsMap[stat.Namespace] = stat.Count
	}

	return statsMap, nil
}

// UpdateNodeMetadata 更新节点元数据
func (d *scfNodeDaoImpl) UpdateNodeMetadata(ctx context.Context, nodeID string, metadata string) error {
	now := time.Now()
	result := d.db.WithContext(ctx).
		Model(&model.SCFNode{}).
		Where("c_node_id = ? AND c_invalid = ?", nodeID, 0).
		Updates(map[string]interface{}{
			"c_metadata": metadata,
			"c_mtime":    now,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update node metadata: %w", result.Error)
	}
	return nil
}

// UpdateNodeStatus 更新节点状态
func (d *scfNodeDaoImpl) UpdateNodeStatus(ctx context.Context, nodeID string, status int) error {
	now := time.Now()
	result := d.db.WithContext(ctx).
		Model(&model.SCFNode{}).
		Where("c_node_id = ? AND c_invalid = ?", nodeID, 0).
		Updates(map[string]interface{}{
			"c_status": status,
			"c_mtime":  now,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update node status: %w", result.Error)
	}
	return nil
}
