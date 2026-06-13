// Package dao 提供数据适配层服务路由接口以及存储相关接口
package dao

import (
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// GetFieldParams 获取字段参数
type GetFieldParams struct {
	// TableID 表ID
	TableID string
	// DataType 数据类型
	DataType pb.EnumDataTypeCategory
	// RowID 行ID（用于静态数据）
	RowID string
	// TimeRange 时间范围（用于时序数据）
	TimeInterval *pb.TimeInterval
	// FieldIDs 要查询的字段ID列表
	FieldIDs []uint32
	// MapKeys 映射键列表
	MapKeys map[uint32]*pb.KeyList
	// MaxLimit 最大返回结果数量限制
	MaxLimit uint32
}

// SearchFieldParams 搜索字段参数
type SearchFieldParams struct {
	// TableID 表ID
	TableID string
	// DataType 数据类型
	DataType pb.EnumDataTypeCategory
	// TimeInterval 时间范围（仅时序数据）
	TimeInterval *pb.TimeInterval
	// TimeSort 时序排序类型（仅时序数据）
	TimeSort pb.Sort
	// RowID 行ID（用于静态数据）
	RowID string
	// SearchOptions 搜索条件
	SearchOptions *pb.SearchOptions
	// PageInfo 分页信息
	PageInfo *pb.PageInfo
}

// SetFieldParams 设置字段参数
type SetFieldParams struct {
	// TableID 表ID
	TableID string
	// DataType 数据类型
	DataType pb.EnumDataTypeCategory
	// UpdateDocRows 更新的文档行
	UpdateDocRows []*pb.UpdateDocRow
	// HistoricalRowsLimit 需要获取的历史数据行数
	HistoricalRowsLimit uint32
}

// DeleteRowsParams 删除行参数
type DeleteRowsParams struct {
	// TableID 表ID
	TableID string
	// DataType 数据类型
	DataType pb.EnumDataTypeCategory
	// TimeInterval 时间范围（用于时序数据批量删除）
	TimeInterval *pb.TimeInterval
	// RowIDs 要删除的行ID列表（用于精确删除）
	RowIDs []string
}

// CreateTableParams 创建表参数
type CreateTableParams struct {
	// TableID 表ID
	TableID string
	// DataType 数据类型(时序数据，还是静态数据；默认静态数据)该字段决定时序字段是否唯一
	DataType pb.EnumDataTypeCategory
	// Description 表描述信息
	Description string
	// ForceCreate 是否强制创建（如果表已存在是否覆盖）
	ForceCreate bool
}
