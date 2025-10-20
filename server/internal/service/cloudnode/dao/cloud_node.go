package dao

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"

	"gorm.io/gorm"
)

// NodeListQuery 节点列表查询参数
type NodeListQuery struct {
	Page     int    // 页码
	PageSize int    // 每页大小
	NodeType string // 节点类型过滤
	Status   string // 状态过滤（online等）
	Keyword  string // 关键字搜索
}

// CloudNodeDAO 节点数据访问对象接口
type CloudNodeDAO interface {
	// ========== 节点查询 ==========

	// GetCloudNodeList 获取云节点列表（支持分页和过滤）
	GetCloudNodeList(ctx context.Context, query *NodeListQuery) ([]*model.CloudNode, int64, error)

	// GetCloudNode 根据节点ID获取云节点
	GetCloudNode(ctx context.Context, nodeID string) (*model.CloudNode, error)

	// GetCloudNodesByType 根据节点类型获取云节点列表
	GetCloudNodesByType(ctx context.Context, nodeType string) ([]*model.CloudNode, error)

	// GetOnlineNodes 获取所有在线节点
	GetOnlineNodes(ctx context.Context) ([]*model.CloudNode, error)

	// GetCloudNodesByRegion 根据区域获取云节点列表
	GetCloudNodesByRegion(ctx context.Context, region string) ([]*model.CloudNode, error)

	// GetNamespaceStats 获取命名空间统计信息
	GetNamespaceStats(ctx context.Context, region string) (map[string]int, error)

	// ========== 节点管理 ==========

	// CreateCloudNode 创建云节点
	CreateCloudNode(ctx context.Context, node *model.CloudNode) error

	// UpdateCloudNode 更新云节点
	UpdateCloudNode(ctx context.Context, node *model.CloudNode) error

	// DeleteCloudNode 删除云节点
	DeleteCloudNode(ctx context.Context, nodeID string) error

	// ========== 节点状态更新 ==========

	// UpdateNodeHeartbeat 更新节点心跳
	UpdateNodeHeartbeat(ctx context.Context, nodeID string, currentLoad string) error


	// UpdateNodeMetadata 更新节点元数据
	UpdateNodeMetadata(ctx context.Context, nodeID string, metadata string) error

	// UpdateNodePackageID 更新节点代码包ID
	UpdateNodePackageID(ctx context.Context, nodeID string, packageID string) error
}

type cloudNodeDaoImpl struct {
	db *gorm.DB
}

// NewCloudNodeDAO 创建新的节点DAO实例
func NewCloudNodeDAO(db *gorm.DB) CloudNodeDAO {
	return &cloudNodeDaoImpl{db: db}
}

// GetCloudNodeList 获取云节点列表（支持分页和过滤）
func (d *cloudNodeDaoImpl) GetCloudNodeList(ctx context.Context, query *NodeListQuery) ([]*model.CloudNode, int64, error) {
	// 设置默认分页参数
	page := query.Page
	if page <= 0 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}

	// 构建查询条件
	db := d.db.WithContext(ctx).Model(&model.CloudNode{}).Where("c_invalid = ?", 0)

	// 应用过滤条件
	if query.NodeType != "" {
		db = db.Where("c_node_type = ?", query.NodeType)
	}

	if query.Keyword != "" {
		keyword := "%" + query.Keyword + "%"
		db = db.Where("c_node_id LIKE ? OR c_region LIKE ? OR c_namespace LIKE ?", keyword, keyword, keyword)
	}

	// 计算总数
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count nodes: %w", err)
	}

	// 分页查询
	var nodes []*model.CloudNode
	offset := (page - 1) * pageSize
	result := db.Order("c_mtime DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&nodes)

	if result.Error != nil {
		return nil, 0, fmt.Errorf("failed to get nodes with pagination: %w", result.Error)
	}

	return nodes, total, nil
}

func (d *cloudNodeDaoImpl) GetCloudNode(ctx context.Context, nodeID string) (*model.CloudNode, error) {
	var node model.CloudNode
	result := d.db.WithContext(ctx).
		Where("c_node_id = ? AND c_invalid = ?", nodeID, 0).
		First(&node)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get collector node: %w", result.Error)
	}
	return &node, nil
}

func (d *cloudNodeDaoImpl) CreateCloudNode(ctx context.Context, node *model.CloudNode) error {
	node.CreateTime = time.Now()
	node.ModifyTime = time.Now()

	result := d.db.WithContext(ctx).Create(node)
	if result.Error != nil {
		return fmt.Errorf("failed to create collector node: %w", result.Error)
	}
	return nil
}

func (d *cloudNodeDaoImpl) UpdateCloudNode(ctx context.Context, node *model.CloudNode) error {
	node.ModifyTime = time.Now()

	result := d.db.WithContext(ctx).
		Model(&model.CloudNode{}).
		Where("c_node_id = ? AND c_invalid = ?", node.NodeID, 0).
		Updates(map[string]interface{}{
			"c_cloud_account_id":     node.CloudAccountID,
			"c_namespace":            node.Namespace,
			"c_node_type":            node.NodeType,
			"c_region":               node.Region,
			"c_ip_address":           node.IPAddress,
			"c_supported_collectors": node.SupportedCollectors,
			"c_metadata":             node.Metadata,
			"c_mtime":                node.ModifyTime,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update collector node: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("node not found or already deleted")
	}
	return nil
}

func (d *cloudNodeDaoImpl) DeleteCloudNode(ctx context.Context, nodeID string) error {
	result := d.db.WithContext(ctx).
		Model(&model.CloudNode{}).
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

func (d *cloudNodeDaoImpl) UpdateNodeHeartbeat(ctx context.Context, nodeID string, currentLoad string) error {
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
		Model(&model.CloudNode{}).
		Where("c_node_id = ? AND c_invalid = ?", nodeID, 0).
		Updates(updates)

	if result.Error != nil {
		return fmt.Errorf("failed to update node heartbeat: %w", result.Error)
	}
	return nil
}

// GetCloudNodesByType 根据节点类型获取节点列表
func (d *cloudNodeDaoImpl) GetCloudNodesByType(ctx context.Context, nodeType string) ([]*model.CloudNode, error) {
	var nodes []*model.CloudNode
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
func (d *cloudNodeDaoImpl) GetOnlineNodes(ctx context.Context) ([]*model.CloudNode, error) {
	var nodes []*model.CloudNode
	result := d.db.WithContext(ctx).
		Where("c_status = ? AND c_invalid = ?", model.NodeStatusOnline, 0).
		Order("c_mtime DESC").
		Find(&nodes)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get online nodes: %w", result.Error)
	}
	return nodes, nil
}


// GetCloudNodesByRegion 根据地区获取节点列表
func (d *cloudNodeDaoImpl) GetCloudNodesByRegion(ctx context.Context, region string) ([]*model.CloudNode, error) {
	var nodes []*model.CloudNode
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
func (d *cloudNodeDaoImpl) GetNamespaceStats(ctx context.Context, region string) (map[string]int, error) {
	type NamespaceCount struct {
		Namespace string
		Count     int
	}

	var stats []NamespaceCount

	// 查询每个命名空间的节点数量
	// 注意：这里假设每个节点代表一个云函数
	result := d.db.WithContext(ctx).
		Model(&model.CloudNode{}).
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
func (d *cloudNodeDaoImpl) UpdateNodeMetadata(ctx context.Context, nodeID string, metadata string) error {
	now := time.Now()
	result := d.db.WithContext(ctx).
		Model(&model.CloudNode{}).
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


// UpdateNodePackageID 更新节点的代码包ID
func (d *cloudNodeDaoImpl) UpdateNodePackageID(ctx context.Context, nodeID string, packageID string) error {
	now := time.Now()
	result := d.db.WithContext(ctx).
		Model(&model.CloudNode{}).
		Where("c_node_id = ? AND c_invalid = ?", nodeID, 0).
		Updates(map[string]interface{}{
			"c_package_id": packageID,
			"c_mtime":      now,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update node package_id: %w", result.Error)
	}
	return nil
}
