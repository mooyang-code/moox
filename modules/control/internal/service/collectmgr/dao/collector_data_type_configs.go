package dao

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/model"

	"gorm.io/gorm"
)

// CollectorDataTypeConfigsDAO 采集器数据类型配置数据访问对象接口
type CollectorDataTypeConfigsDAO interface {
	// GetDataTypeConfigs 获取所有激活的数据类型配置
	GetDataTypeConfigs(ctx context.Context) ([]*model.CollectorDataTypeConfig, error)

	// GetDataTypeConfigByType 根据数据类型获取配置
	GetDataTypeConfigByType(ctx context.Context, dataType string) (*model.CollectorDataTypeConfig, error)
}

// collectorDataTypeConfigsDAO 实现采集器数据类型配置数据访问对象
type collectorDataTypeConfigsDAO struct {
	db *gorm.DB
}

// NewCollectorDataTypeConfigsDAO 创建采集器数据类型配置数据访问对象
func NewCollectorDataTypeConfigsDAO(db *gorm.DB) CollectorDataTypeConfigsDAO {
	return &collectorDataTypeConfigsDAO{
		db: db,
	}
}

// GetDataTypeConfigs 获取所有激活的数据类型配置
func (dao *collectorDataTypeConfigsDAO) GetDataTypeConfigs(ctx context.Context) ([]*model.CollectorDataTypeConfig, error) {
	var configs []*model.CollectorDataTypeConfig

	err := dao.db.WithContext(ctx).
		Where("c_invalid = ?", 0).
		Order("c_sort_order ASC, c_ctime ASC").
		Find(&configs).Error

	if err != nil {
		return nil, err
	}
	return configs, nil
}

// GetDataTypeConfigByType 根据数据类型获取配置
func (dao *collectorDataTypeConfigsDAO) GetDataTypeConfigByType(ctx context.Context, dataType string) (*model.CollectorDataTypeConfig, error) {
	var config model.CollectorDataTypeConfig

	err := dao.db.WithContext(ctx).
		Where("c_data_type = ? AND c_invalid = ?", dataType, 0).
		First(&config).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &config, nil
}
