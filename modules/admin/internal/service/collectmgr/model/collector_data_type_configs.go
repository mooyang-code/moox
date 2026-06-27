package model

import (
	"time"
)

// CollectorDataTypeConfig 采集器数据类型配置表
type CollectorDataTypeConfig struct {
	// ID 主键ID
	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// DataType 数据类型标识 (kline/ticker/orderbook/trade/news)
	DataType string `gorm:"column:c_data_type;uniqueIndex:idx_collector_data_type_configs_data_type;not null" json:"data_type"`
	// TypeName 数据类型显示名称 (K线数据/Ticker数据等)
	TypeName string `gorm:"column:c_type_name;not null" json:"type_name"`
	// TypeDesc 数据类型描述
	TypeDesc string `gorm:"column:c_type_desc;not null;default:''" json:"type_desc"`
	// DataSourceOptions 数据源选项
	DataSourceOptions string `gorm:"column:c_data_source_options;not null;default:'{}'" json:"data_source_options"`
	// SortOrder 排序顺序
	SortOrder int `gorm:"column:c_sort_order;not null;default:0" json:"sort_order"`
	// Version 配置版本号
	Version int `gorm:"column:c_version;not null;default:1" json:"version"`
	// IsDeleted 软删除标记
	IsDeleted string `gorm:"column:c_is_deleted;not null;default:'false'" json:"is_deleted"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

// TableName 指定表名
func (c *CollectorDataTypeConfig) TableName() string {
	return "t_collector_data_type_configs"
}
