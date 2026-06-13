package model

// Dataset 数据集定义表结构
type Dataset struct {
	// ID 自增ID
	ID int `json:"_id" yaml:"id" gorm:"column:c_id;primaryKey;autoIncrement"`
	// DatasetID 数据集ID
	DatasetID int `json:"dataset_id" yaml:"dataset_id" gorm:"column:c_dataset_id;primaryKey;not null;default:0"`
	// DatasetName 数据集名
	DatasetName string `json:"dataset_name" yaml:"dataset_name" gorm:"column:c_dataset_name;type:varchar(250);index;not null;default:''"`
	// ObjectTableID 数据集对应的对象表ID
	ObjectTableID string `json:"object_table_id" yaml:"object_table_id" gorm:"column:c_object_table_id;type:varchar(250);not null;default:''"`
	// DataTableID 数据集对应的数据表ID
	DataTableID string `json:"data_table_id" yaml:"data_table_id" gorm:"column:c_data_table_id;type:varchar(250);not null;default:''"`
	// ProjID 所属项目ID
	ProjID int `json:"proj_id" yaml:"proj_id" gorm:"column:c_proj_id;index;not null;default:0"`
	// DataType 数据类型（取值见:common.proto-EnumDataType）
	DataType int `json:"data_type" yaml:"data_type" gorm:"column:c_data_type;index;not null;default:0"`
	// Freqs 时序周期（多值用+分割）
	Freqs string `json:"freqs" yaml:"freqs" gorm:"column:c_freqs;type:varchar(250);default:''"`
	// CheckRules 数据完整性校验规则（内置的校验规则名）
	CheckRules string `json:"check_rules" yaml:"check_rules" gorm:"column:c_check_rules;type:varchar(250);not null;default:''"`
	// Comment 备注
	Comment string `json:"comment" yaml:"comment" gorm:"column:c_comment;type:text"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `json:"enabled" yaml:"enabled" gorm:"column:c_enabled;type:text;not null;default:'true'"`
	// CreateTime 创建时间
	CreateTime string `json:"create_time" yaml:"create_time" gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP;index"`
	// ModifyTime 修改时间
	ModifyTime string `json:"modify_time" yaml:"modify_time" gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP;index"`
}

const DatasetTableName = "t_dataset"

// TableName 指定表名
func (d *Dataset) TableName() string {
	return DatasetTableName
}
