package model

import "time"

// Field 字段表定义
type Field struct {
	// ID 自增主键
	ID uint `gorm:"primaryKey;column:c_id;autoIncrement" json:"id" yaml:"id"`
	// FieldID 字段ID
	FieldID int `gorm:"column:c_field_id;uniqueIndex:idx_field_id;not null;default:0" json:"field_id" yaml:"field_id"`
	// ProjID 项目ID
	ProjID int `gorm:"column:c_proj_id;index:idx_proj_id;not null;default:0" json:"proj_id" yaml:"proj_id"`
	// DatasetIDs 数据集ID列表，以逗号分隔
	DatasetIDs string `gorm:"column:c_dataset_ids;type:text;not null;default:''" json:"dataset_ids" yaml:"dataset_ids"`
	// FieldName 字段名称
	FieldName string `gorm:"column:c_field_name;index:idx_field_name;size:100;not null;default:''" json:"field_name" yaml:"field_name"`
	// InterfaceName 接口名称
	InterfaceName string `gorm:"column:c_interface_name;uniqueIndex:idx_interface_name;size:100;not null;default:''" json:"interface_name" yaml:"interface_name"`
	// Desc 字段描述
	Desc string `gorm:"column:c_desc;type:text;not null;default:''" json:"desc" yaml:"desc"`
	// TableType 字段所属表类型（1=数据对象表，2=数据表）
	TableType int `gorm:"column:c_table_type;not null;default:1" json:"table_type" yaml:"table_type"`
	// Required 是否必填字段（"true"=是，"false"=否）
	Required string `gorm:"column:c_required;type:text;not null;default:'false'" json:"required" yaml:"required"`
	// Unique 是否唯一字段（"true"=是，"false"=否）
	Unique string `gorm:"column:c_unique;type:text;not null;default:'false'" json:"unique" yaml:"unique"`
	// ParentFieldID 父字段ID
	ParentFieldID int `gorm:"column:c_parent_field_id;not null;default:0" json:"parent_field_id" yaml:"parent_field_id"`
	// LevelInfo 层级信息
	LevelInfo string `gorm:"column:c_level_info;type:text;not null;default:''" json:"level_info" yaml:"level_info"`
	// FieldPrimaryFormat 字段主要格式
	FieldPrimaryFormat int `gorm:"column:c_field_primary_format;not null;default:0" json:"field_primary_format" yaml:"field_primary_format"`
	// FieldSecondaryFormat 字段次要格式
	FieldSecondaryFormat int `gorm:"column:c_field_secondary_format;not null;default:0" json:"field_secondary_format" yaml:"field_secondary_format"`
	// ValueLibID 值库ID
	ValueLibID int `gorm:"column:c_value_lib_id;not null;default:0" json:"value_lib_id" yaml:"value_lib_id"`
	// ValidationRule 验证规则
	ValidationRule string `gorm:"column:c_validation_rule;type:text;not null;default:''" json:"validation_rule" yaml:"validation_rule"`
	// WriteExample 写入示例
	WriteExample string `gorm:"column:c_write_example;type:text;not null;default:''" json:"write_example" yaml:"write_example"`
	// Remark 备注信息
	Remark string `gorm:"column:c_remark;type:text" json:"remark" yaml:"remark"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `gorm:"column:c_enabled;type:text;not null;default:'true'" json:"enabled" yaml:"enabled"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time" yaml:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time" yaml:"modify_time"`
}

const FieldTableName = "t_field"

// TableName 指定表名
func (f *Field) TableName() string {
	return FieldTableName
}
