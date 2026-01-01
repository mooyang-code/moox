package dao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"

	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// NodeListQuery 节点列表查询参数
type NodeListQuery struct {
	Page     int    // 页码
	PageSize int    // 每页大小
	NodeType string // 节点类型过滤
	Status   string // 状态过滤（online等）
	Keyword  string // 关键字搜索
}

// NodeStatusFilter 节点状态过滤参数
// 用于任务分配时过滤节点状态
type NodeStatusFilter struct {
	Status *int // 节点状态，nil 表示不过滤
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

	// ========== 任务分配相关查询 ==========

	// GetNodesBySupportedCollector 获取支持指定采集器类型的节点
	// 查询条件：c_supported_collectors 包含指定的 collectorType
	// filter: 可选，传入则按状态过滤；不传或为nil则不过滤状态
	GetNodesBySupportedCollector(ctx context.Context, collectorType string, filter *NodeStatusFilter) ([]*model.CloudNode, error)

	// GetNodesByPattern 根据节点ID通配符匹配获取节点
	// pattern 中的 * 会被转换为 SQL LIKE 的 %
	// filter: 可选，传入则按状态过滤；不传或为nil则不过滤状态
	GetNodesByPattern(ctx context.Context, pattern string, filter *NodeStatusFilter) ([]*model.CloudNode, error)

	// GetNodesByIDs 根据节点ID列表获取节点
	// filter: 可选，传入则按状态过滤；不传或为nil则不过滤状态
	GetNodesByIDs(ctx context.Context, nodeIDs []string, filter *NodeStatusFilter) ([]*model.CloudNode, error)

	// ========== 节点管理 ==========

	// CreateCloudNode 创建云节点
	CreateCloudNode(ctx context.Context, node *model.CloudNode) error

	// UpdateCloudNode 更新云节点
	UpdateCloudNode(ctx context.Context, node *model.CloudNode) error

	// DeleteCloudNode 删除云节点
	DeleteCloudNode(ctx context.Context, nodeID string) error

	// UpdateNodePackageID 更新节点代码包ID
	UpdateNodePackageID(ctx context.Context, nodeID string, packageID string) error

	// UpdateSupportedCollectors 更新节点支持的采集器类型
	UpdateSupportedCollectors(ctx context.Context, nodeID string, collectors []string) error
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
			"c_timeout_threshold":    node.TimeoutThreshold,
			"c_heartbeat_interval":   node.HeartbeatInterval,
			"c_probe_enabled":        node.ProbeEnabled,
			"c_probe_url":            node.ProbeURL,
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
// 基于心跳表的最后心跳时间判断节点是否在线
func (d *cloudNodeDaoImpl) GetOnlineNodes(ctx context.Context) ([]*model.CloudNode, error) {
	var nodes []*model.CloudNode

	// 使用 JOIN 查询，基于心跳表判断在线状态
	// 判断逻辑：当前时间 - 最后心跳时间 < 超时阈值（默认35秒）
	// 注意：c_timeout_threshold = 0 表示使用默认值，需要用 CASE WHEN 处理
	result := d.db.WithContext(ctx).
		Table("t_cloud_nodes cn").
		Select("cn.*").
		Joins("INNER JOIN t_heartbeat_nodes hn ON cn.c_node_id = hn.c_node_id").
		Where("cn.c_invalid = ? AND hn.c_invalid = ?", 0, 0).
		Where("(JULIANDAY('now') - JULIANDAY(hn.c_last_heartbeat)) * 86400 < CASE WHEN cn.c_timeout_threshold = 0 THEN 35 ELSE cn.c_timeout_threshold END").
		Order("cn.c_mtime DESC").
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

// GetNodesBySupportedCollector 获取支持指定采集器类型的节点
// 查询条件：c_supported_collectors 包含指定的 collectorType（JSON数组格式）
// filter: 可选，传入则按状态过滤；不传或为nil则不过滤状态
func (d *cloudNodeDaoImpl) GetNodesBySupportedCollector(ctx context.Context, collectorType string, filter *NodeStatusFilter) ([]*model.CloudNode, error) {
	var nodes []*model.CloudNode
	// c_supported_collectors 是 JSON 数组格式，如：["kline", "ticker"]
	// 使用 LIKE 查询包含指定类型的节点
	pattern := fmt.Sprintf("%%\"%s\"%%", collectorType)

	query := d.db.WithContext(ctx).Table("t_cloud_nodes cn")

	// 如果需要按在线状态过滤，则 JOIN 心跳表
	if filter != nil && filter.Status != nil {
		query = query.Select("cn.*").
			Joins("INNER JOIN t_heartbeat_nodes hn ON cn.c_node_id = hn.c_node_id").
			Where("cn.c_supported_collectors LIKE ? AND cn.c_invalid = ? AND hn.c_invalid = ?", pattern, 0, 0).
			Where("(JULIANDAY('now') - JULIANDAY(hn.c_last_heartbeat)) * 86400 < CASE WHEN cn.c_timeout_threshold = 0 THEN 35 ELSE cn.c_timeout_threshold END")
	} else {
		query = query.Where("c_supported_collectors LIKE ? AND c_invalid = ?", pattern, 0)
	}

	result := query.Order("c_mtime DESC").Find(&nodes)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get nodes by supported collector: %w", result.Error)
	}
	return nodes, nil
}

// GetNodesByPattern 根据节点ID通配符匹配获取节点
// pattern 中的 * 会被转换为 SQL LIKE 的 %
// filter: 可选，传入则按状态过滤；不传或为nil则不过滤状态
func (d *cloudNodeDaoImpl) GetNodesByPattern(ctx context.Context, pattern string, filter *NodeStatusFilter) ([]*model.CloudNode, error) {
	var nodes []*model.CloudNode
	// 将通配符 * 转换为 SQL LIKE 的 %
	sqlPattern := pattern
	// 如果 pattern 不包含 % 或 *，则不做转换（精确匹配）
	// 如果包含 *，则转换为 %
	if pattern != "" {
		sqlPattern = strings.ReplaceAll(pattern, "*", "%")
	}

	query := d.db.WithContext(ctx).Table("t_cloud_nodes cn")

	// 如果需要按在线状态过滤，则 JOIN 心跳表
	if filter != nil && filter.Status != nil {
		query = query.Select("cn.*").
			Joins("INNER JOIN t_heartbeat_nodes hn ON cn.c_node_id = hn.c_node_id").
			Where("cn.c_node_id LIKE ? AND cn.c_invalid = ? AND hn.c_invalid = ?", sqlPattern, 0, 0).
			Where("(JULIANDAY('now') - JULIANDAY(hn.c_last_heartbeat)) * 86400 < CASE WHEN cn.c_timeout_threshold = 0 THEN 35 ELSE cn.c_timeout_threshold END")
	} else {
		query = query.Where("c_node_id LIKE ? AND c_invalid = ?", sqlPattern, 0)
	}

	result := query.Order("c_mtime DESC").Find(&nodes)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get nodes by pattern: %w", result.Error)
	}
	return nodes, nil
}

// GetNodesByIDs 根据节点ID列表获取节点
// filter: 可选，传入则按状态过滤；不传或为nil则不过滤状态
func (d *cloudNodeDaoImpl) GetNodesByIDs(ctx context.Context, nodeIDs []string, filter *NodeStatusFilter) ([]*model.CloudNode, error) {
	if len(nodeIDs) == 0 {
		return []*model.CloudNode{}, nil
	}

	var nodes []*model.CloudNode
	query := d.db.WithContext(ctx).Table("t_cloud_nodes cn")

	// 如果需要按在线状态过滤，则 JOIN 心跳表
	if filter != nil && filter.Status != nil {
		query = query.Select("cn.*").
			Joins("INNER JOIN t_heartbeat_nodes hn ON cn.c_node_id = hn.c_node_id").
			Where("cn.c_node_id IN ? AND cn.c_invalid = ? AND hn.c_invalid = ?", nodeIDs, 0, 0).
			Where("(JULIANDAY('now') - JULIANDAY(hn.c_last_heartbeat)) * 86400 < CASE WHEN cn.c_timeout_threshold = 0 THEN 35 ELSE cn.c_timeout_threshold END")
	} else {
		query = query.Where("c_node_id IN ? AND c_invalid = ?", nodeIDs, 0)
	}

	result := query.Order("c_mtime DESC").Find(&nodes)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get nodes by IDs: %w", result.Error)
	}
	return nodes, nil
}

// UpdateSupportedCollectors 更新节点支持的采集器类型
func (d *cloudNodeDaoImpl) UpdateSupportedCollectors(ctx context.Context, nodeID string, collectors []string) error {
	// 序列化为 JSON
	collectorsJSON, err := json.Marshal(collectors)
	if err != nil {
		return fmt.Errorf("序列化采集器类型失败: %w", err)
	}

	// 更新数据库
	result := d.db.WithContext(ctx).
		Model(&model.CloudNode{}).
		Where("c_node_id = ? AND c_invalid = ?", nodeID, 0).
		Update("c_supported_collectors", string(collectorsJSON))

	if result.Error != nil {
		return fmt.Errorf("更新采集器类型失败: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		// 节点不存在或已失效，记录警告但不报错
		log.WarnContextf(ctx, "[CloudNodeDAO] 节点 %s 不存在或已失效，跳过更新采集器类型", nodeID)
	} else {
		log.InfoContextf(ctx, "[CloudNodeDAO] 节点 %s 的采集器类型已更新: %v", nodeID, collectors)
	}

	return nil
}
