package sqlite

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// Field 字段表定义
type Field model.Field

// TableName 指定表名
func (f Field) TableName() string {
	return model.FieldTableName
}

// GetFieldItems 获取字段表数据
func (d *dataDBImpl) GetFieldItems(interfaceName string) ([]model.Field, error) {
	var fields []model.Field
	// 如果interfaceName为空，则获取所有字段
	if interfaceName == "" {
		return d.GetValidFieldList()
	}
	result := d.db.Where("c_interface_name = ?", interfaceName).Find(&fields)
	if result.Error != nil {
		log.Errorf("GetFieldItems err[%v]", result.Error)
		return nil, result.Error
	}
	return fields, nil
}

// GetValidFieldList 获取所有合法字段
func (d *dataDBImpl) GetValidFieldList() ([]model.Field, error) {
	var fields []model.Field
	result := d.db.Where("c_enabled = ?", constants.EnabledValue).Find(&fields)
	if result.Error != nil {
		log.Errorf("GetValidFieldList err[%v]", result.Error)
		return nil, result.Error
	}
	return fields, nil
}

// GetAllFieldList 获取所有字段信息，包括已禁用的字段
func (d *dataDBImpl) GetAllFieldList() ([]model.Field, error) {
	var fields []model.Field
	result := d.db.Find(&fields)
	if result.Error != nil {
		log.Errorf("GetAllFieldList err[%v]", result.Error)
		return nil, result.Error
	}
	return fields, nil
}

// AddField 添加字段
func (d *dataDBImpl) AddField(field *model.Field) error {
	if field.Enabled == "" {
		field.Enabled = constants.EnabledValue
	}
	return d.db.Debug().Create(field).Error
}

func (d *dataDBImpl) UpdateField(field *model.Field) error {
	field.ModifyTime = time.Now()
	return d.db.Debug().Model(&model.Field{}).
		Where("c_field_id = ?", field.FieldID).
		Updates(field).Error
}

func (d *dataDBImpl) DeleteField(fieldID int) error {
	return d.db.Model(&model.Field{}).
		Where("c_field_id = ? AND c_enabled = ?", fieldID, constants.EnabledValue).
		Update("c_enabled", constants.DisabledValue).Error
}

// GetMaxFieldIDInRange 获取指定范围内的最大字段ID
func (d *dataDBImpl) GetMaxFieldIDInRange(minID, maxID int) (int, error) {
	var result int
	err := d.db.Table("t_field").
		Where("c_field_id >= ? AND c_field_id <= ?", minID, maxID).
		Select("COALESCE(MAX(c_field_id), ?)", minID-1).
		Scan(&result).Error
	if err != nil {
		return 0, fmt.Errorf("获取最大字段ID失败: %w", err)
	}
	return result, nil
}
