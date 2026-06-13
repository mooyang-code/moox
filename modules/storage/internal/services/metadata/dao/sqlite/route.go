package sqlite

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// ObjectRoute 数据对象路由表定义
type ObjectRoute model.ObjectRoute

// TableName 指定表名
func (o ObjectRoute) TableName() string {
	return model.ObjectRouteTableName
}

// FieldRoute 字段路由表定义
type FieldRoute model.FieldRoute

// TableName 指定表名
func (f FieldRoute) TableName() string {
	return model.FieldRouteTableName
}

// GetObjectRouteList 获取所有数据对象路由列表
func (d *dataDBImpl) GetObjectRouteList() ([]model.ObjectRoute, error) {
	var routes []model.ObjectRoute
	result := d.db.Where("c_enabled = ?", constants.EnabledValue).Find(&routes)
	if result.Error != nil {
		log.Errorf("GetObjectRouteList err[%v]", result.Error)
		return nil, result.Error
	}
	return routes, nil
}

// GetObjectRouteByID 根据ID获取数据对象路由
func (d *dataDBImpl) GetObjectRouteByID(routeID int) (*model.ObjectRoute, error) {
	var route model.ObjectRoute
	result := d.db.Where("c_id = ? AND c_enabled = ?", routeID, constants.EnabledValue).First(&route)
	if result.Error != nil {
		log.Errorf("GetObjectRouteByID err[%v]", result.Error)
		return nil, result.Error
	}
	return &route, nil
}

// GetObjectRouteByDatasetID 根据数据集ID获取数据对象路由列表
func (d *dataDBImpl) GetObjectRouteByDatasetID(datasetID int) ([]model.ObjectRoute, error) {
	var routes []model.ObjectRoute
	result := d.db.Where("c_dataset_id = ? AND c_enabled = ?", datasetID, constants.EnabledValue).Find(&routes)
	if result.Error != nil {
		log.Errorf("GetObjectRouteByDatasetID err[%v]", result.Error)
		return nil, result.Error
	}
	return routes, nil
}

// GetObjectRouteByEntityID 根据存储实体ID获取数据对象路由列表
func (d *dataDBImpl) GetObjectRouteByEntityID(entityID int) ([]model.ObjectRoute, error) {
	var routes []model.ObjectRoute
	result := d.db.Where("c_entity_id = ? AND c_enabled = ?", entityID, constants.EnabledValue).Find(&routes)
	if result.Error != nil {
		log.Errorf("GetObjectRouteByEntityID err[%v]", result.Error)
		return nil, result.Error
	}
	return routes, nil
}

// GetObjectRouteListWithFilter 支持分页和过滤的数据对象路由查询
func (d *dataDBImpl) GetObjectRouteListWithFilter(projectID, datasetID, entityID int, limit, offset int) ([]model.ObjectRoute, int, error) {
	var routes []model.ObjectRoute
	var total int64

	query := d.db.Model(&model.ObjectRoute{}).Where("c_enabled = ?", constants.EnabledValue)

	// 添加过滤条件
	if projectID > 0 {
		query = query.Where("c_project_id = ?", projectID)
	}
	if datasetID > 0 {
		query = query.Where("c_dataset_id = ?", datasetID)
	}
	if entityID > 0 {
		query = query.Where("c_entity_id = ?", entityID)
	}

	// 获取总数
	if err := query.Count(&total).Error; err != nil {
		log.Errorf("GetObjectRouteListWithFilter count err[%v]", err)
		return nil, 0, err
	}

	// 分页查询
	result := query.Limit(limit).Offset(offset).Find(&routes)
	if result.Error != nil {
		log.Errorf("GetObjectRouteListWithFilter err[%v]", result.Error)
		return nil, 0, result.Error
	}

	return routes, int(total), nil
}

// AddObjectRoute 添加新的数据对象路由
func (d *dataDBImpl) AddObjectRoute(route *model.ObjectRoute) error {
	if route.Enabled == "" {
		route.Enabled = constants.EnabledValue
	}
	result := d.db.Create(route)
	if result.Error != nil {
		log.Errorf("AddObjectRoute err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// UpdateObjectRoute 更新数据对象路由信息
func (d *dataDBImpl) UpdateObjectRoute(route *model.ObjectRoute) error {
	result := d.db.Model(&model.ObjectRoute{}).Where("c_id = ?", route.ID).
		Updates(map[string]interface{}{
			"c_dataset_id": route.DatasetID,
			"c_object_id":  route.ObjectID,
			"c_entity_id":  route.EntityID,
		})
	if result.Error != nil {
		log.Errorf("UpdateObjectRoute err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// DeleteObjectRoute 逻辑删除数据对象路由
func (d *dataDBImpl) DeleteObjectRoute(routeID int) error {
	result := d.db.Model(&model.ObjectRoute{}).Where("c_id = ?", routeID).
		Update("c_enabled", constants.DisabledValue)
	if result.Error != nil {
		log.Errorf("DeleteObjectRoute err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// GetFieldRouteList 获取所有字段路由列表
func (d *dataDBImpl) GetFieldRouteList() ([]model.FieldRoute, error) {
	var routes []model.FieldRoute
	result := d.db.Where("c_enabled = ?", constants.EnabledValue).Find(&routes)
	if result.Error != nil {
		log.Errorf("GetFieldRouteList err[%v]", result.Error)
		return nil, result.Error
	}
	return routes, nil
}

// GetFieldRouteByID 根据ID获取字段路由
func (d *dataDBImpl) GetFieldRouteByID(routeID int) (*model.FieldRoute, error) {
	var route model.FieldRoute
	result := d.db.Where("c_id = ? AND c_enabled = ?", routeID, constants.EnabledValue).First(&route)
	if result.Error != nil {
		log.Errorf("GetFieldRouteByID err[%v]", result.Error)
		return nil, result.Error
	}
	return &route, nil
}

// GetFieldRouteByFieldID 根据字段ID获取字段路由列表
func (d *dataDBImpl) GetFieldRouteByFieldID(fieldID int) ([]model.FieldRoute, error) {
	var routes []model.FieldRoute
	result := d.db.Where("c_field_id = ? AND c_enabled = ?", fieldID, constants.EnabledValue).Find(&routes)
	if result.Error != nil {
		log.Errorf("GetFieldRouteByFieldID err[%v]", result.Error)
		return nil, result.Error
	}
	return routes, nil
}

// GetFieldRouteByDeviceID 根据存储设备ID获取字段路由列表
func (d *dataDBImpl) GetFieldRouteByDeviceID(deviceID int) ([]model.FieldRoute, error) {
	var routes []model.FieldRoute
	result := d.db.Where("c_device_id = ? AND c_enabled = ?", deviceID, constants.EnabledValue).Find(&routes)
	if result.Error != nil {
		log.Errorf("GetFieldRouteByDeviceID err[%v]", result.Error)
		return nil, result.Error
	}
	return routes, nil
}

// GetFieldRouteListWithFilter 支持分页和过滤的字段路由查询
func (d *dataDBImpl) GetFieldRouteListWithFilter(projectID, fieldID, datasetID, deviceID int, limit, offset int) ([]model.FieldRoute, int, error) {
	var routes []model.FieldRoute
	var total int64

	query := d.db.Model(&model.FieldRoute{}).Where("c_enabled = ?", constants.EnabledValue)

	// 添加过滤条件
	if projectID > 0 {
		query = query.Where("c_project_id = ?", projectID)
	}
	if fieldID > 0 {
		query = query.Where("c_field_id = ?", fieldID)
	}
	if datasetID >= 0 {
		query = query.Where("c_dataset_id = ?", datasetID)
	}
	if deviceID > 0 {
		query = query.Where("c_device_id = ?", deviceID)
	}

	// 获取总数
	if err := query.Count(&total).Error; err != nil {
		log.Errorf("GetFieldRouteListWithFilter count err[%v]", err)
		return nil, 0, err
	}

	// 分页查询
	result := query.Limit(limit).Offset(offset).Find(&routes)
	if result.Error != nil {
		log.Errorf("GetFieldRouteListWithFilter err[%v]", result.Error)
		return nil, 0, result.Error
	}
	return routes, int(total), nil
}

// AddFieldRoute 添加新的字段路由
func (d *dataDBImpl) AddFieldRoute(route *model.FieldRoute) error {
	if route.Enabled == "" {
		route.Enabled = constants.EnabledValue
	}
	result := d.db.Create(route)
	if result.Error != nil {
		log.Errorf("AddFieldRoute err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// UpdateFieldRoute 更新字段路由信息
func (d *dataDBImpl) UpdateFieldRoute(route *model.FieldRoute) error {
	result := d.db.Model(&model.FieldRoute{}).Where("c_id = ?", route.ID).
		Updates(map[string]interface{}{
			"c_field_id":   route.FieldID,
			"c_dataset_id": route.DatasetID,
			"c_device_id":  route.DeviceID,
		})
	if result.Error != nil {
		log.Errorf("UpdateFieldRoute err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// DeleteFieldRoute 禁用字段路由（设置enabled=false）
func (d *dataDBImpl) DeleteFieldRoute(routeID int) error {
	result := d.db.Model(&model.FieldRoute{}).Where("c_id = ?", routeID).
		Update("c_enabled", constants.DisabledValue)
	if result.Error != nil {
		log.Errorf("DeleteFieldRoute err[%v]", result.Error)
		return result.Error
	}
	return nil
}
