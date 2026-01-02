package dao

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	cloudnodeconfig "github.com/mooyang-code/moox/server/internal/service/cloudnode/config"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"
	"trpc.group/trpc-go/trpc-go/log"

	"gorm.io/gorm"
)

// HeartbeatDAO 心跳记录数据访问对象接口
type HeartbeatDAO interface {
	// Create 创建记录
	Create(ctx context.Context, record *types.HeartbeatNode) error

	// GetNodeByID 根据节点ID获取记录
	GetNodeByID(ctx context.Context, nodeID string) (*types.HeartbeatNode, error)

	// Update 更新记录
	Update(ctx context.Context, record *types.HeartbeatNode) error

	// Delete 删除记录(软删除)
	Delete(ctx context.Context, id int64) error

	// List 列出记录
	List(ctx context.Context, filter *types.NodeFilter) ([]*types.HeartbeatNode, int64, error)

	// GetTimeoutNodes 获取超时节点
	GetTimeoutNodes(ctx context.Context) ([]*types.HeartbeatNode, error)

	// BatchUpdate 批量更新记录
	BatchUpdate(ctx context.Context, records []*types.HeartbeatNode) error

	// GetNodeStatus 根据节点ID获取节点状态
	GetNodeStatus(ctx context.Context, nodeID string) (*types.NodeStatus, error)
}

// heartbeatRecordDAO 心跳记录DAO实现
type heartbeatRecordDAO struct {
	db *gorm.DB
}

// NewHeartbeatNodeDAO 创建心跳记录DAO
func NewHeartbeatNodeDAO(db *gorm.DB) HeartbeatDAO {
	return &heartbeatRecordDAO{
		db: db,
	}
}

// Create 创建记录
func (d *heartbeatRecordDAO) Create(ctx context.Context, record *types.HeartbeatNode) error {
	modelRecord := &model.HeartbeatNode{}
	modelRecord.FromDTO(record)

	if err := d.db.WithContext(ctx).Create(modelRecord).Error; err != nil {
		return fmt.Errorf("create heartbeat record failed: %w", err)
	}

	// 更新ID
	record.ID = modelRecord.ID
	return nil
}

// GetByID 根据ID获取记录
func (d *heartbeatRecordDAO) GetByID(ctx context.Context, id int64) (*types.HeartbeatNode, error) {
	var modelRecord model.HeartbeatNode

	if err := d.db.WithContext(ctx).
		Where("c_id = ? AND c_invalid = 0", id).
		First(&modelRecord).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get heartbeat record by id failed: %w", err)
	}
	return modelRecord.ToDTO(), nil
}

// GetNodeByID 根据节点ID获取记录
func (d *heartbeatRecordDAO) GetNodeByID(ctx context.Context, nodeID string) (*types.HeartbeatNode, error) {
	var modelRecord model.HeartbeatNode

	if err := d.db.WithContext(ctx).
		Where("c_node_id = ? AND c_invalid = 0", nodeID).
		First(&modelRecord).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get heartbeat record by node id failed: %w", err)
	}
	return modelRecord.ToDTO(), nil
}

// Update 更新记录
func (d *heartbeatRecordDAO) Update(ctx context.Context, record *types.HeartbeatNode) error {
	modelRecord := &model.HeartbeatNode{}
	modelRecord.FromDTO(record)

	if err := d.db.WithContext(ctx).
		Where("c_id = ? AND c_invalid = 0", record.ID).
		Updates(modelRecord).Error; err != nil {
		return fmt.Errorf("update heartbeat record failed: %w", err)
	}
	return nil
}

// Delete 删除记录(软删除)
func (d *heartbeatRecordDAO) Delete(ctx context.Context, id int64) error {
	if err := d.db.WithContext(ctx).
		Model(&model.HeartbeatNode{}).
		Where("c_id = ?", id).
		Update("c_invalid", 1).Error; err != nil {
		return fmt.Errorf("delete heartbeat record failed: %w", err)
	}
	return nil
}

// List 列出记录
func (d *heartbeatRecordDAO) List(ctx context.Context, filter *types.NodeFilter) ([]*types.HeartbeatNode, int64, error) {
	if filter == nil {
		filter = &types.NodeFilter{}
	}
	filter.SetDefaults()

	query := d.db.WithContext(ctx).Table("t_heartbeat_nodes hn").Where("hn.c_invalid = 0")

	// 应用过滤条件
	query = d.applyNodeFilter(query, filter)

	if filter.Status != nil {
		defaultTimeout := cloudnodeconfig.Get().Heartbeat.DefaultTimeoutThreshold
		query = query.Joins("LEFT JOIN t_cloud_nodes cn ON hn.c_node_id = cn.c_node_id AND cn.c_invalid = 0")

		switch *filter.Status {
		case types.NodeStatusOnline:
			query = query.Where("hn.c_last_heartbeat IS NOT NULL").
				Where("(JULIANDAY('now') - JULIANDAY(hn.c_last_heartbeat)) * 86400 < CASE WHEN cn.c_timeout_threshold IS NULL OR cn.c_timeout_threshold = 0 THEN ? ELSE cn.c_timeout_threshold END", defaultTimeout)
		case types.NodeStatusOffline:
			query = query.Where("hn.c_last_heartbeat IS NULL OR (JULIANDAY('now') - JULIANDAY(hn.c_last_heartbeat)) * 86400 >= CASE WHEN cn.c_timeout_threshold IS NULL OR cn.c_timeout_threshold = 0 THEN ? ELSE cn.c_timeout_threshold END", defaultTimeout)
		default:
			query = query.Where("1 = 0")
		}
	}

	// 计算总数
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count heartbeat records failed: %w", err)
	}

	// 应用分页和排序
	offset := (filter.GetPage() - 1) * filter.GetPageSize()
	query = query.Select("hn.*").Offset(offset).Limit(filter.GetPageSize())

	if filter.SortBy != "" {
		order := filter.SortBy
		if !strings.Contains(order, ".") {
			order = "hn." + order
		}
		if filter.SortOrder == "desc" {
			order += " DESC"
		} else {
			order += " ASC"
		}
		query = query.Order(order)
	} else {
		query = query.Order("hn.c_mtime DESC")
	}

	var modelRecords []model.HeartbeatNode
	if err := query.Find(&modelRecords).Error; err != nil {
		return nil, 0, fmt.Errorf("list heartbeat records failed: %w", err)
	}

	// 对于List方法，我们需要额外获取节点配置信息
	// 为简化起见，先获取所有记录，然后再查询节点配置
	now := time.Time{}
	var records []*types.HeartbeatNode
	for _, modelRecord := range modelRecords {
		record := modelRecord.ToDTO()

		// 计算基于心跳时间的实时状态
		if now.IsZero() {
			now = time.Now()
		}
		record.Status = d.calculateNodeStatus(ctx, record, now)

		records = append(records, record)
	}
	return records, total, nil
}

// GetTimeoutNodes 获取超时节点
func (d *heartbeatRecordDAO) GetTimeoutNodes(ctx context.Context) ([]*types.HeartbeatNode, error) {
	// 获取默认超时阈值
	defaultTimeout := cloudnodeconfig.Get().Heartbeat.DefaultTimeoutThreshold

	// 使用较小值作为超时判断阈值（更严格的超时检测）
	minTimeoutThreshold := d.getMinTimeoutThreshold()
	timeoutThreshold := min(minTimeoutThreshold, defaultTimeout)
	log.DebugContextf(ctx, "GetTimeoutNodes timeoutThreshold:%d", timeoutThreshold)

	// 直接查询心跳记录，筛选超时节点（没有心跳记录或超过更严格的时间阈值）
	var results []model.HeartbeatNode

	// 使用指定的格式计算超时时间
	timeoutTime := time.Now().Add(-time.Duration(timeoutThreshold) * time.Second).Format("2006-01-02 15:04:05.000000000+07:00")
	if err := d.db.WithContext(ctx).
		Where("c_last_heartbeat < ? AND c_invalid = 0", timeoutTime).
		Find(&results).Error; err != nil {
		log.ErrorContextf(ctx, "GetTimeoutNodes query failed: %v", err)
		return nil, fmt.Errorf("get timeout heartbeat records failed: %w", err)
	}

	// 转换为types记录并设置状态
	var records []*types.HeartbeatNode
	for _, result := range results {
		record := result.ToDTO()
		record.Status = types.NodeStatusTimeout
		records = append(records, record)
	}
	return records, nil
}

// getMinTimeoutThreshold 获取所有节点中配置的最小超时阈值
func (d *heartbeatRecordDAO) getMinTimeoutThreshold() int {
	type result struct {
		TimeoutThreshold int
	}

	var results []result
	// 查询所有节点的超时阈值配置
	err := d.db.Model(&model.CloudNode{}).
		Where("c_invalid = 0 AND c_timeout_threshold > 0").
		Pluck("c_timeout_threshold", &results).Error

	if err != nil || len(results) == 0 {
		// 如果没有配置或查询失败，返回默认值
		return cloudnodeconfig.Get().Heartbeat.DefaultTimeoutThreshold
	}

	// 找出最小值
	minTimeout := results[0].TimeoutThreshold
	for _, r := range results {
		if r.TimeoutThreshold < minTimeout {
			minTimeout = r.TimeoutThreshold
		}
	}
	return minTimeout
}

// min 辅助函数，返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// BatchUpdate 批量更新记录
func (d *heartbeatRecordDAO) BatchUpdate(ctx context.Context, records []*types.HeartbeatNode) error {
	if len(records) == 0 {
		return nil
	}

	return d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, record := range records {
			modelRecord := &model.HeartbeatNode{}
			modelRecord.FromDTO(record)

			if err := tx.Where("c_id = ? AND c_invalid = 0", record.ID).
				Updates(modelRecord).Error; err != nil {
				return fmt.Errorf("batch update heartbeat record %d failed: %w", record.ID, err)
			}
		}
		return nil
	})
}

// applyNodeFilter 应用节点过滤条件
func (d *heartbeatRecordDAO) applyNodeFilter(query *gorm.DB, filter *types.NodeFilter) *gorm.DB {
	if len(filter.NodeIDs) > 0 {
		query = query.Where("hn.c_node_id IN ?", filter.NodeIDs)
	}

	if len(filter.NodeTypes) > 0 {
		query = query.Where("hn.c_node_type IN ?", filter.NodeTypes)
	}

	if filter.SourceService != nil {
		query = query.Where("hn.c_source_service = ?", *filter.SourceService)
	}

	if filter.ProbeEnabled != nil {
		query = query.Where("hn.c_probe_enabled = ?", *filter.ProbeEnabled)
	}

	if filter.Keyword != "" {
		keyword := "%" + filter.Keyword + "%"
		query = query.Where("hn.c_node_id LIKE ? OR hn.c_source_service LIKE ?", keyword, keyword)
	}
	return query
}

// calculateNodeStatus 基于最后心跳时间计算节点状态
func (d *heartbeatRecordDAO) calculateNodeStatus(ctx context.Context, record *types.HeartbeatNode, now time.Time) types.NodeStatus {
	// 如果没有心跳记录，认为是离线
	if record.LastHeartbeat == nil {
		return types.NodeStatusOffline
	}

	// 智能获取超时阈值：优先使用节点配置，其次使用默认配置
	timeoutThreshold := d.getTimeoutThresholdForNode(ctx, record.NodeID)

	// 计算距离最后心跳的时间
	timeSinceLastHeartbeat := now.Sub(*record.LastHeartbeat)

	// 如果超过阈值，认为是超时（离线）
	if timeSinceLastHeartbeat > time.Duration(timeoutThreshold)*time.Second {
		return types.NodeStatusOffline
	}

	// 否则认为是在线
	return types.NodeStatusOnline
}

// getTimeoutThresholdForNode 获取节点的超时阈值
func (d *heartbeatRecordDAO) getTimeoutThresholdForNode(ctx context.Context, nodeID string) int {
	// 查询节点的配置
	var nodeConfig struct {
		TimeoutThreshold int `gorm:"column:c_timeout_threshold"`
	}

	err := d.db.WithContext(ctx).
		Table("t_cloud_nodes").
		Select("c_timeout_threshold").
		Where("c_node_id = ? AND c_invalid = 0", nodeID).
		First(&nodeConfig).Error

	if err == nil && nodeConfig.TimeoutThreshold > 0 {
		return nodeConfig.TimeoutThreshold
	}

	// 如果没有配置或配置为0，使用默认配置
	return cloudnodeconfig.Get().Heartbeat.DefaultTimeoutThreshold
}

// GetNodeStatus 根据节点ID获取节点状态（基于最后心跳时间判断）
func (d *heartbeatRecordDAO) GetNodeStatus(ctx context.Context, nodeID string) (*types.NodeStatus, error) {
	// 获取心跳记录
	var record model.HeartbeatNode
	if err := d.db.WithContext(ctx).
		Where("c_node_id = ? AND c_invalid = 0", nodeID).
		First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 如果心跳表中没有记录，则认为状态为离线
			offline := types.NodeStatusOffline
			return &offline, nil
		}
		return nil, fmt.Errorf("get node status failed: %w", err)
	}

	// 基于最后心跳时间计算状态
	typesRecord := record.ToDTO()
	status := d.calculateNodeStatus(ctx, typesRecord, time.Now())
	return &status, nil
}
