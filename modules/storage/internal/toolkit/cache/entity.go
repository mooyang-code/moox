package cache

import (
	"fmt"
	"strconv"
)

const TBEntity = "t_storage_entity"

// StorageEntity 存储实体表结构(存储实体表记录存储实体的连接信息，提供给access层使用)
type StorageEntity struct {
	// EntityID 存储实体ID
	EntityID int `json:"entity_id"`
	// EntitySrvConn 存储实体的连接信息（格式： ip://127.0.0.1:xxxx）
	EntitySrvConn string `json:"entity_srv_conn"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `json:"enabled"`
	// AccessUrl 访问该表的接口url
	AccessUrl string
}

// SchemaID 实现接口TableCacher
func (StorageEntity) SchemaID() string {
	return TBEntity
}

// URL 实现接口TableCacher
func (s StorageEntity) URL() string {
	return s.AccessUrl
}

// SearchFields 实现接口TableCacher
func (StorageEntity) SearchFields() map[string]string {
	return map[string]string{TBEntity: "entity_id"}
}

// FilterKey 实现接口TableCacher
func (StorageEntity) FilterKey() string {
	return "enabled=true"
}

// GetStorageEntityInfo 获取存储实体配置缓存
func GetStorageEntityInfo(entityID int) *StorageEntity {
	entity, ok := QueryDataItem(TBEntity, "entity_id="+strconv.Itoa(entityID)).(*StorageEntity)
	if !ok {
		return nil
	}
	return entity
}

// GetAllStorageEntityInfo 获取所有存储实体配置缓存
func GetAllStorageEntityInfo() []*StorageEntity {
	entities, ok := GetAll(TBEntity).([]*StorageEntity)
	if !ok {
		return nil
	}
	return entities
}

// GetStorageEntityByID 根据存储实体ID获取存储实体信息
func GetStorageEntityByID(entityID int) (*StorageEntity, error) {
	entity := GetStorageEntityInfo(entityID)
	if entity == nil {
		return nil, fmt.Errorf("存储实体[%d]不存在", entityID)
	}
	return entity, nil
}
