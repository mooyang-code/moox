package model

// FieldColumnMap 字段-DB列名映射表
type FieldColumnMap struct {
	// ID 自增ID
	ID int `json:"_id" yaml:"id" gorm:"column:c_id;primaryKey;autoIncrement"`
	// ProjectID 项目ID
	ProjectID int `json:"project_id" yaml:"project_id" gorm:"column:c_project_id;not null;default:0;uniqueIndex:idx_field_project_table;uniqueIndex:idx_column_project_table"`
	// TableType 表类型（1数据对象表；2数据表）
	TableType int `json:"table_type" yaml:"table_type" gorm:"column:c_table_type;not null;default:0;uniqueIndex:idx_field_project_table;uniqueIndex:idx_column_project_table"`
	// FieldID 字段ID
	FieldID int `json:"field_id" yaml:"field_id" gorm:"column:c_field_id;not null;default:0;uniqueIndex:idx_field_project_table"`
	// ColumnName 底层列名
	ColumnName string `json:"column_name" yaml:"column_name" gorm:"column:c_column_name;type:text;not null;default:'';uniqueIndex:idx_column_project_table"`

	// CreateTime 创建时间
	CreateTime string `json:"create_time" yaml:"create_time" gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP"`
	// ModifyTime 修改时间
	ModifyTime string `json:"modify_time" yaml:"modify_time" gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP"`
}

const FieldColumnMapTableName = "t_field_column_map"

// TableName 指定表名
func (s *FieldColumnMap) TableName() string {
	return FieldColumnMapTableName
}
