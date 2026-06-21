package space

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// DAO 封装 Space 相关表的显式读写操作。
type DAO struct {
	db *gorm.DB
}

// NewDAO 创建 Space DAO。
func NewDAO(db *gorm.DB) *DAO { return &DAO{db: db} }

// CreateSpace 新建 Space。
func (d *DAO) CreateSpace(ctx context.Context, item *Space) error {
	now := time.Now()
	item.CreatedAt = now
	item.UpdatedAt = now
	if item.Status == "" {
		item.Status = "active"
	}
	if item.Attributes == "" {
		item.Attributes = "{}"
	}
	return d.db.WithContext(ctx).Create(item).Error
}

// UpdateSpace 更新 Space 的管理台展示属性。
func (d *DAO) UpdateSpace(ctx context.Context, item *Space) error {
	if item.SpaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	if item.Status == "" {
		item.Status = "active"
	}
	if item.Attributes == "" {
		item.Attributes = "{}"
	}
	result := d.db.WithContext(ctx).Model(&Space{}).
		Where("c_space_id = ? AND c_invalid = 0", item.SpaceID).
		Updates(map[string]interface{}{
			"c_name":        item.Name,
			"c_description": item.Description,
			"c_owner":       item.Owner,
			"c_market":      item.Market,
			"c_timezone":    item.Timezone,
			"c_status":      item.Status,
			"c_attributes":  item.Attributes,
			"c_mtime":       time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("space not found: %s", item.SpaceID)
	}
	return nil
}

// ListSpaces 按 owner/status 分页查询有效 Space。
func (d *DAO) ListSpaces(ctx context.Context, owner string, status string, offset int, limit int) ([]Space, int64, error) {
	query := d.db.WithContext(ctx).Model(&Space{}).Where("c_invalid = 0")
	if owner != "" {
		query = query.Where("c_owner = ?", owner)
	}
	if status != "" {
		query = query.Where("c_status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Space
	if err := query.Order("c_mtime DESC, c_id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListSpaceMembers 分页查询指定 Space 下的成员。
func (d *DAO) ListSpaceMembers(ctx context.Context, spaceID string, offset int, limit int) ([]SpaceMember, int64, error) {
	query := d.db.WithContext(ctx).Model(&SpaceMember{}).Where("c_space_id = ?", spaceID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []SpaceMember
	if err := query.Order("c_mtime DESC, c_id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
