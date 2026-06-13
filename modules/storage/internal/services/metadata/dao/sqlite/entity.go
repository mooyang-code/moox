package sqlite

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// Entity 存储实体表定义
type Entity model.StorageEntity

// TableName 指定表名
func (e Entity) TableName() string {
	return model.StorageEntityTableName
}

// GetEntityList 获取所有存储实体列表
func (d *dataDBImpl) GetEntityList() ([]model.StorageEntity, error) {
	var entities []model.StorageEntity
	result := d.db.Where("c_enabled = ?", constants.EnabledValue).Find(&entities)
	if result.Error != nil {
		log.Errorf("GetEntityList err[%v]", result.Error)
		return nil, result.Error
	}
	return entities, nil
}

// GetEntityByID 根据ID获取存储实体
func (d *dataDBImpl) GetEntityByID(entityID int) (*model.StorageEntity, error) {
	var entity model.StorageEntity
	result := d.db.Where("c_entity_id = ? AND c_enabled = ?", entityID, constants.EnabledValue).First(&entity)
	if result.Error != nil {
		log.Errorf("GetEntityByID err[%v]", result.Error)
		return nil, result.Error
	}
	return &entity, nil
}

// AddEntity 添加新的存储实体
func (d *dataDBImpl) AddEntity(entity *model.StorageEntity) error {
	if entity.Enabled == "" {
		entity.Enabled = constants.EnabledValue
	}
	result := d.db.Create(entity)
	if result.Error != nil {
		log.Errorf("AddEntity err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// UpdateEntity 更新存储实体信息
func (d *dataDBImpl) UpdateEntity(entity *model.StorageEntity) error {
	result := d.db.Model(&model.StorageEntity{}).Where("c_entity_id = ?", entity.EntityID).
		Updates(map[string]interface{}{
			"c_entity_alias":    entity.EntityAlias,
			"c_entity_srv_conn": entity.EntitySrvConn,
		})
	if result.Error != nil {
		log.Errorf("UpdateEntity err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// DeleteEntity 逻辑删除存储实体
func (d *dataDBImpl) DeleteEntity(entityID int) error {
	result := d.db.Model(&model.StorageEntity{}).Where("c_entity_id = ?", entityID).
		Update("c_enabled", constants.DisabledValue)
	if result.Error != nil {
		log.Errorf("DeleteEntity err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// GetMaxEntityID 获取当前最大的存储实体ID
func (d *dataDBImpl) GetMaxEntityID() (int, error) {
	var maxID int
	result := d.db.Model(&model.StorageEntity{}).Select("COALESCE(MAX(c_entity_id), 0)").Scan(&maxID)
	if result.Error != nil {
		log.Errorf("GetMaxEntityID err[%v]", result.Error)
		return 0, result.Error
	}
	return maxID, nil
}

// IsEntityReferencedByObjectRoute 检查存储实体是否被数据对象路由引用
func (d *dataDBImpl) IsEntityReferencedByObjectRoute(entityID int) (bool, error) {
	var count int64
	result := d.db.Model(&model.ObjectRoute{}).Where("c_entity_id = ? AND c_enabled = ?",
		entityID, constants.EnabledValue).Count(&count)
	if result.Error != nil {
		log.Errorf("IsEntityReferencedByObjectRoute err[%v]", result.Error)
		return false, result.Error
	}
	return count > 0, nil
}
