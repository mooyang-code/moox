package model

// StorageEntity 存储实体表结构
type StorageEntity struct {
	// ID 自增ID
	ID int `json:"_id" yaml:"id" gorm:"column:c_id;primaryKey;autoIncrement"`
	// EntityID 存储实体ID
	EntityID int `json:"entity_id" yaml:"entity_id" gorm:"column:c_entity_id;uniqueIndex;not null;default:0"`
	// EntityAlias 存储实体别名
	EntityAlias string `json:"entity_alias" yaml:"entity_alias" gorm:"column:c_entity_alias;type:varchar(250);not null;default:'0'"`
	// EntitySrvConn 存储实体的连接信息
	EntitySrvConn string `json:"entity_srv_conn" yaml:"entity_srv_conn" gorm:"column:c_entity_srv_conn;type:varchar(250);not null;default:''"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `json:"enabled" yaml:"enabled" gorm:"column:c_enabled;type:text;not null;default:'true'"`
	// CreateTime 创建时间
	CreateTime string `json:"create_time" yaml:"create_time" gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP"`
	// ModifyTime 修改时间
	ModifyTime string `json:"modify_time" yaml:"modify_time" gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP"`
}

const StorageEntityTableName = "t_storage_entity"

// TableName 指定表名
func (s *StorageEntity) TableName() string {
	return StorageEntityTableName
}
