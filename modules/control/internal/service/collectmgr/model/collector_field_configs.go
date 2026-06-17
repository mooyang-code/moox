package model

import (
	"time"
)

// CollectorFieldConfig 采集器参数字段配置表
type CollectorFieldConfig struct {
	// ID 主键ID
	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// DataType 关联的数据类型 (kline/ticker/orderbook等)
	DataType string `gorm:"column:c_data_type;index:idx_collector_field_configs_data_type;not null" json:"data_type"`
	// FieldKey 字段标识 (intervals/objects/depth等)
	FieldKey string `gorm:"column:c_field_key;not null" json:"field_key"`
	// FieldName 字段显示名称 (时间周期/交易对象等)
	FieldName string `gorm:"column:c_field_name;not null" json:"field_name"`
	// FieldType 字段类型 (text/number/select/multi-select/checkbox/array)
	FieldType string `gorm:"column:c_field_type;not null" json:"field_type"`
	// IsRequired 是否必填
	IsRequired bool `gorm:"column:c_is_required;not null;default:false" json:"is_required"`
	// DefaultValue 默认值
	DefaultValue string `gorm:"column:c_default_value;not null;default:''" json:"default_value"`
	// FieldOptions 字段选项
	FieldOptions string `gorm:"column:c_field_options;not null;default:''" json:"field_options"`
	// DataSourceOptions 数据源选项
	DataSourceOptions string `gorm:"column:c_data_source_options;not null;default:''" json:"data_source_options"`
	// SortOrder 字段排序
	SortOrder int `gorm:"column:c_sort_order;not null;default:0" json:"sort_order"`
	// Invalid 删除标记
	Invalid int `gorm:"column:c_invalid;not null;default:0" json:"invalid"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

// TableName 指定表名
func (c *CollectorFieldConfig) TableName() string {
	return "t_collector_field_configs"
}
