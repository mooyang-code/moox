package dao

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"
)

// HeartbeatDAO 心跳记录数据访问对象接口
type HeartbeatDAO interface {
	// Create 创建记录
	Create(ctx context.Context, record *types.HeartbeatNode) error

	// GetByID 根据ID获取记录
	GetByID(ctx context.Context, id int64) (*types.HeartbeatNode, error)

	// GetByNode 根据节点ID和类型获取记录
	GetByNode(ctx context.Context, nodeID, nodeType string) (*types.HeartbeatNode, error)

	// Update 更新记录
	Update(ctx context.Context, record *types.HeartbeatNode) error

	// Delete 删除记录(软删除)
	Delete(ctx context.Context, id int64) error

	// List 列出记录
	List(ctx context.Context, filter *types.NodeFilter) ([]*types.HeartbeatNode, int64, error)

	// GetByStatus 根据状态获取记录
	GetByStatus(ctx context.Context, status types.NodeStatus) ([]*types.HeartbeatNode, error)

	// BatchUpdate 批量更新记录
	BatchUpdate(ctx context.Context, records []*types.HeartbeatNode) error

	// GetNodeStatus 根据节点ID获取节点状态
	GetNodeStatus(ctx context.Context, nodeID string) (*types.NodeStatus, error)
}

// heartbeatRecordDAO 心跳记录DAO实现
type heartbeatRecordDAO struct {
	db *gorm.DB
}

// NewHeartbeatRecordDAO 创建心跳记录DAO
func NewHeartbeatRecordDAO(db *gorm.DB) HeartbeatDAO {
	return &heartbeatRecordDAO{db: db}
}

// Create 创建记录
func (d *heartbeatRecordDAO) Create(ctx context.Context, record *types.HeartbeatNode) error {
	modelRecord := &model.HeartbeatNode{}
	modelRecord.FromTypesRecord(record)

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
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("get heartbeat record by id failed: %w", err)
	}

	return modelRecord.ToTypesRecord(), nil
}

// GetByNode 根据节点ID和类型获取记录
func (d *heartbeatRecordDAO) GetByNode(ctx context.Context, nodeID, nodeType string) (*types.HeartbeatNode, error) {
	var modelRecord model.HeartbeatNode

	if err := d.db.WithContext(ctx).
		Where("c_node_id = ? AND c_node_type = ? AND c_invalid = 0", nodeID, nodeType).
		First(&modelRecord).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("get heartbeat record by node failed: %w", err)
	}

	return modelRecord.ToTypesRecord(), nil
}

// Update 更新记录
func (d *heartbeatRecordDAO) Update(ctx context.Context, record *types.HeartbeatNode) error {
	modelRecord := &model.HeartbeatNode{}
	modelRecord.FromTypesRecord(record)

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

	query := d.db.WithContext(ctx).Model(&model.HeartbeatNode{}).Where("c_invalid = 0")

	// 应用过滤条件
	query = d.applyNodeFilter(query, filter)

	// 计算总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count heartbeat records failed: %w", err)
	}

	// 应用分页和排序
	offset := (filter.GetPage() - 1) * filter.GetPageSize()
	query = query.Offset(offset).Limit(filter.GetPageSize())

	if filter.SortBy != "" {
		order := filter.SortBy
		if filter.SortOrder == "desc" {
			order += " DESC"
		} else {
			order += " ASC"
		}
		query = query.Order(order)
	} else {
		query = query.Order("c_mtime DESC")
	}

	var modelRecords []model.HeartbeatNode
	if err := query.Find(&modelRecords).Error; err != nil {
		return nil, 0, fmt.Errorf("list heartbeat records failed: %w", err)
	}

	// 转换为types记录
	records := make([]*types.HeartbeatNode, len(modelRecords))
	for i, modelRecord := range modelRecords {
		records[i] = modelRecord.ToTypesRecord()
	}

	return records, total, nil
}

// GetByStatus 根据状态获取记录
func (d *heartbeatRecordDAO) GetByStatus(ctx context.Context, status types.NodeStatus) ([]*types.HeartbeatNode, error) {
	var modelRecords []model.HeartbeatNode

	if err := d.db.WithContext(ctx).
		Where("c_status = ? AND c_invalid = 0", status).
		Find(&modelRecords).Error; err != nil {
		return nil, fmt.Errorf("get heartbeat records by status failed: %w", err)
	}

	records := make([]*types.HeartbeatNode, len(modelRecords))
	for i, modelRecord := range modelRecords {
		records[i] = modelRecord.ToTypesRecord()
	}

	return records, nil
}

// BatchUpdate 批量更新记录
func (d *heartbeatRecordDAO) BatchUpdate(ctx context.Context, records []*types.HeartbeatNode) error {
	if len(records) == 0 {
		return nil
	}

	return d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, record := range records {
			modelRecord := &model.HeartbeatNode{}
			modelRecord.FromTypesRecord(record)

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
		query = query.Where("c_node_id IN ?", filter.NodeIDs)
	}

	if len(filter.NodeTypes) > 0 {
		query = query.Where("c_node_type IN ?", filter.NodeTypes)
	}

	if filter.SourceService != nil {
		query = query.Where("c_source_service = ?", *filter.SourceService)
	}

	if filter.Status != nil {
		query = query.Where("c_status = ?", *filter.Status)
	}

	if filter.ProbeEnabled != nil {
		query = query.Where("c_probe_enabled = ?", *filter.ProbeEnabled)
	}

	if filter.Keyword != "" {
		keyword := "%" + filter.Keyword + "%"
		query = query.Where("c_node_id LIKE ? OR c_source_service LIKE ?", keyword, keyword)
	}

	return query
}

// GetNodeStatus 根据节点ID获取节点状态
func (d *heartbeatRecordDAO) GetNodeStatus(ctx context.Context, nodeID string) (*types.NodeStatus, error) {
	var record model.HeartbeatNode

	if err := d.db.WithContext(ctx).
		Select("c_status").
		Where("c_node_id = ? AND c_invalid = 0", nodeID).
		First(&record).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			// 如果心跳表中没有记录，则认为状态为离线
			offline := types.NodeStatusOffline
			return &offline, nil
		}
		return nil, fmt.Errorf("get node status failed: %w", err)
	}

	return &record.Status, nil
}
