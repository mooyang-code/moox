package dao

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/admin/internal/common"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/model"

	"gorm.io/gorm"
)

// CollectorFieldConfigsDAO 采集器参数字段配置数据访问对象接口
type CollectorFieldConfigsDAO interface {
	// GetFieldConfigsByDataType 根据数据类型获取所有字段配置
	GetFieldConfigsByDataType(ctx context.Context, dataType string) ([]*model.CollectorFieldConfig, error)

	// GetFieldConfigByTypeAndKey 根据数据类型和字段键获取配置
	GetFieldConfigByTypeAndKey(ctx context.Context, dataType, fieldKey string) (*model.CollectorFieldConfig, error)
}

// collectorFieldConfigsDAO 实现采集器参数字段配置数据访问对象
type collectorFieldConfigsDAO struct {
	db *gorm.DB
}

// NewCollectorFieldConfigsDAO 创建采集器参数字段配置数据访问对象
func NewCollectorFieldConfigsDAO(db *gorm.DB) CollectorFieldConfigsDAO {
	return &collectorFieldConfigsDAO{
		db: db,
	}
}

// GetFieldConfigsByDataType 根据数据类型获取所有字段配置
func (dao *collectorFieldConfigsDAO) GetFieldConfigsByDataType(ctx context.Context, dataType string) ([]*model.CollectorFieldConfig, error) {
	var configs []*model.CollectorFieldConfig

	err := dao.db.WithContext(ctx).
		Where("c_data_type = ? AND c_is_deleted != ?", dataType, common.IsDeletedTrue).
		Order("c_sort_order ASC, c_ctime ASC").
		Find(&configs).Error

	if err != nil {
		return nil, err
	}
	return configs, nil
}

// GetFieldConfigByTypeAndKey 根据数据类型和字段键获取配置
func (dao *collectorFieldConfigsDAO) GetFieldConfigByTypeAndKey(ctx context.Context, dataType, fieldKey string) (*model.CollectorFieldConfig, error) {
	var config model.CollectorFieldConfig

	err := dao.db.WithContext(ctx).
		Where("c_data_type = ? AND c_field_key = ? AND c_is_deleted != ?", dataType, fieldKey, common.IsDeletedTrue).
		First(&config).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &config, nil
}
