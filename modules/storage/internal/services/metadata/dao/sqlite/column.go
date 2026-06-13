package sqlite

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// FieldColumnMap 字段-DB列名映射表定义
type FieldColumnMap model.FieldColumnMap

// TableName 指定表名
func (f FieldColumnMap) TableName() string {
	return model.FieldColumnMapTableName
}

// GetColumnMapList 获取所有字段列名映射列表
func (d *dataDBImpl) GetColumnMapList() ([]model.FieldColumnMap, error) {
	var columns []model.FieldColumnMap
	result := d.db.Find(&columns)
	if result.Error != nil {
		log.Errorf("GetColumnMapList err[%v]", result.Error)
		return nil, result.Error
	}
	return columns, nil
}

// GetColumnMapByFieldID 根据字段ID获取列名映射
func (d *dataDBImpl) GetColumnMapByFieldID(fieldID int) ([]model.FieldColumnMap, error) {
	var columns []model.FieldColumnMap
	result := d.db.Where("c_field_id = ?", fieldID).Find(&columns)
	if result.Error != nil {
		log.Errorf("GetColumnMapByFieldID err[%v]", result.Error)
		return nil, result.Error
	}
	return columns, nil
}

// GetColumnMapByProjectID 根据项目ID获取列名映射
func (d *dataDBImpl) GetColumnMapByProjectID(projectID int) ([]model.FieldColumnMap, error) {
	var columns []model.FieldColumnMap
	result := d.db.Where("c_project_id = ?", projectID).Find(&columns)
	if result.Error != nil {
		log.Errorf("GetColumnMapByProjectID err[%v]", result.Error)
		return nil, result.Error
	}
	return columns, nil
}

// GetColumnMapByProjectAndType 根据项目ID和表类型获取列名映射
func (d *dataDBImpl) GetColumnMapByProjectAndType(projectID, tableType int) ([]model.FieldColumnMap, error) {
	var columns []model.FieldColumnMap
	result := d.db.Where("c_project_id = ? AND c_table_type = ?", projectID, tableType).Find(&columns)
	if result.Error != nil {
		log.Errorf("GetColumnMapByProjectAndType err[%v]", result.Error)
		return nil, result.Error
	}
	return columns, nil
}

// GetColumnMapByFieldProjectAndType 根据字段ID、项目ID和表类型获取列名映射
func (d *dataDBImpl) GetColumnMapByFieldProjectAndType(fieldID, projectID, tableType int) (*model.FieldColumnMap, error) {
	var column model.FieldColumnMap
	result := d.db.Where("c_field_id = ? AND c_project_id = ? AND c_table_type = ?", fieldID, projectID, tableType).First(&column)
	if result.Error != nil {
		log.Errorf("GetColumnMapByFieldProjectAndType err[%v]", result.Error)
		return nil, result.Error
	}
	return &column, nil
}

// AddColumnMap 添加新的字段列名映射
func (d *dataDBImpl) AddColumnMap(column *model.FieldColumnMap) error {
	result := d.db.Create(column)
	if result.Error != nil {
		log.Errorf("AddColumnMap err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// UpdateColumnMap 更新字段列名映射
func (d *dataDBImpl) UpdateColumnMap(column *model.FieldColumnMap) error {
	result := d.db.Save(column)
	if result.Error != nil {
		log.Errorf("UpdateColumnMap err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// DeleteColumnMap 删除字段列名映射（物理删除）
func (d *dataDBImpl) DeleteColumnMap(id int) error {
	result := d.db.Delete(&model.FieldColumnMap{}, id)
	if result.Error != nil {
		log.Errorf("DeleteColumnMap err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// DeleteColumnMapByFieldID 根据字段ID删除所有相关的列名映射
func (d *dataDBImpl) DeleteColumnMapByFieldID(fieldID int) error {
	result := d.db.Where("c_field_id = ?", fieldID).Delete(&model.FieldColumnMap{})
	if result.Error != nil {
		log.Errorf("DeleteColumnMapByFieldID err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// DeleteColumnMapByProjectID 根据项目ID删除所有相关的列名映射
func (d *dataDBImpl) DeleteColumnMapByProjectID(projectID int) error {
	result := d.db.Where("c_project_id = ?", projectID).Delete(&model.FieldColumnMap{})
	if result.Error != nil {
		log.Errorf("DeleteColumnMapByProjectID err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// DeleteColumnMapByProjectAndType 根据项目ID和表类型删除所有相关的列名映射
func (d *dataDBImpl) DeleteColumnMapByProjectAndType(projectID, tableType int) error {
	result := d.db.Where("c_project_id = ? AND c_table_type = ?", projectID, tableType).Delete(&model.FieldColumnMap{})
	if result.Error != nil {
		log.Errorf("DeleteColumnMapByProjectAndType err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// BatchAddColumnMaps 批量添加字段列名映射
func (d *dataDBImpl) BatchAddColumnMaps(columns []model.FieldColumnMap) error {
	if len(columns) == 0 {
		return nil
	}
	result := d.db.Create(&columns)
	if result.Error != nil {
		log.Errorf("BatchAddColumnMaps err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// GetColumnNameByFieldProjectAndType 根据字段ID、项目ID和表类型获取列名
func (d *dataDBImpl) GetColumnNameByFieldProjectAndType(fieldID, projectID, tableType int) (string, error) {
	var column model.FieldColumnMap
	result := d.db.Select("c_column_name").Where("c_field_id = ? AND c_project_id = ? AND c_table_type = ?", fieldID, projectID, tableType).First(&column)
	if result.Error != nil {
		log.Errorf("GetColumnNameByFieldProjectAndType err[%v]", result.Error)
		return "", result.Error
	}
	return column.ColumnName, nil
}

// GetFieldIDsByProjectTypeAndColumn 根据项目ID、表类型和列名获取字段ID列表
func (d *dataDBImpl) GetFieldIDsByProjectTypeAndColumn(projectID, tableType int, columnName string) ([]int, error) {
	var columns []model.FieldColumnMap
	result := d.db.Select("c_field_id").Where("c_project_id = ? AND c_table_type = ? AND c_column_name = ?", projectID, tableType, columnName).Find(&columns)
	if result.Error != nil {
		log.Errorf("GetFieldIDsByProjectTypeAndColumn err[%v]", result.Error)
		return nil, result.Error
	}

	var fieldIDs []int
	for _, column := range columns {
		fieldIDs = append(fieldIDs, column.FieldID)
	}
	return fieldIDs, nil
}

// IsColumnMapExists 检查字段列名映射是否存在
func (d *dataDBImpl) IsColumnMapExists(fieldID, projectID, tableType int) (bool, error) {
	var count int64
	result := d.db.Model(&model.FieldColumnMap{}).Where("c_field_id = ? AND c_project_id = ? AND c_table_type = ?", fieldID, projectID, tableType).Count(&count)
	if result.Error != nil {
		log.Errorf("IsColumnMapExists err[%v]", result.Error)
		return false, result.Error
	}
	return count > 0, nil
}
