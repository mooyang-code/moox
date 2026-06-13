package model

// ObjectRoute 数据对象路由表结构(水平切分：系统支持不同的数据对象路由至不同的存储实体)
type ObjectRoute struct {
	// ID 自增ID
	ID int `json:"_id" yaml:"id" gorm:"column:c_id;primaryKey;autoIncrement"`
	// ProjectID 项目ID
	ProjectID int `json:"project_id" yaml:"project_id" gorm:"column:c_project_id;not null;default:0"`
	// DatasetID 数据集ID
	DatasetID int `json:"dataset_id" yaml:"dataset_id" gorm:"column:c_dataset_id;index;not null;default:0"`
	// ObjectID 数据对象ID（*表示所有）
	ObjectID string `json:"object_id" yaml:"object_id" gorm:"column:c_object_id;type:varchar(250);not null;default:''"`
	// EntityID 存储实体ID
	EntityID int `json:"entity_id" yaml:"entity_id" gorm:"column:c_entity_id;index;not null;default:0"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `json:"enabled" yaml:"enabled" gorm:"column:c_enabled;type:text;not null;default:'true'"`
	// CreateTime 创建时间
	CreateTime string `json:"create_time" yaml:"create_time" gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP;index"`
	// ModifyTime 修改时间
	ModifyTime string `json:"modify_time" yaml:"modify_time" gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP;index"`
}

const ObjectRouteTableName = "t_object_route"

// TableName 指定表名
func (o *ObjectRoute) TableName() string {
	return ObjectRouteTableName
}

// FieldRoute 字段路由表结构（纵向切分：系统支持不同字段路由至不同的存储设备）
type FieldRoute struct {
	// ID 自增ID
	ID int `json:"_id" yaml:"id" gorm:"column:c_id;primaryKey;autoIncrement"`
	// FieldID 字段ID
	FieldID int `json:"field_id" yaml:"field_id" gorm:"column:c_field_id;not null;default:0"`
	// ProjectID 项目ID
	ProjectID int `json:"project_id" yaml:"project_id" gorm:"column:c_project_id;not null;default:0"`
	// DatasetID 数据集ID（为0表示该项目下所有的数据集）
	DatasetID int `json:"dataset_id" yaml:"dataset_id" gorm:"column:c_dataset_id;not null;default:0"`
	// DeviceID 字段的存储设备ID
	DeviceID int `json:"device_id" yaml:"device_id" gorm:"column:c_device_id;not null;default:0"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `json:"enabled" yaml:"enabled" gorm:"column:c_enabled;type:text;not null;default:'true'"`
	// CreateTime 创建时间
	CreateTime string `json:"create_time" yaml:"create_time" gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP;index"`
	// ModifyTime 修改时间
	ModifyTime string `json:"modify_time" yaml:"modify_time" gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP;index"`
}

const FieldRouteTableName = "t_field_route"

// TableName 指定表名
func (f *FieldRoute) TableName() string {
	return FieldRouteTableName
}
