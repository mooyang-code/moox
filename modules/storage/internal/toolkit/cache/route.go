package cache

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
)

const TBFieldRoute = "t_field_route"
const TBObjectRoute = "t_object_route"

// FieldRoute 字段路由表结构
type FieldRoute struct {
	// ID 自增ID
	ID int `json:"_id"`
	// FieldID 字段ID
	FieldID int `json:"field_id"`
	// DatasetID 数据集ID（为0表示该项目下所有的数据集）
	DatasetID int `json:"dataset_id"`
	// DeviceID 字段的存储设备ID
	DeviceID int `json:"device_id"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `json:"enabled"`
	// AccessUrl 访问该表的接口url
	AccessUrl string
	// PrjID 项目ID（字段所属的项目ID，方便前端根据项目区分）
	PrjID int `json:"project_id"`
}

// SchemaID 实现接口TableCacher
func (FieldRoute) SchemaID() string {
	return TBFieldRoute
}

// URL 实现接口TableCacher
func (f FieldRoute) URL() string {
	return f.AccessUrl
}

// SearchFields 实现接口TableCacher
func (FieldRoute) SearchFields() map[string]string {
	return map[string]string{TBFieldRoute: "_id"}
}

// FilterKey 实现接口TableCacher
func (FieldRoute) FilterKey() string {
	return "enabled=true"
}

// GetFieldRouteByPrjID 通过项目ID获取字段路由配置
func GetFieldRouteByPrjID(prjID int) ([]*FieldRoute, error) {
	allRoutes, ok := GetAll(TBFieldRoute).([]*FieldRoute)
	if !ok {
		return nil, fmt.Errorf("获取所有路由配置失败")
	}

	var result []*FieldRoute
	for _, route := range allRoutes {
		if route.PrjID == prjID && route.Enabled == constants.EnabledValue {
			result = append(result, route)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("项目[%d]没有配置字段路由", prjID)
	}
	return result, nil
}

// ObjectRoute 对象路由表结构
type ObjectRoute struct {
	// ID 自增ID
	ID int `json:"_id"`
	// DatasetID 数据集ID
	DatasetID int `json:"dataset_id"`
	// ObjectID 数据对象ID
	ObjectID string `json:"object_id"`
	// EntityID 存储实体ID
	EntityID int `json:"entity_id"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `json:"enabled"`
	// AccessUrl 访问该表的接口url
	AccessUrl string
	// PrjID 项目ID（字段所属的项目ID，方便前端根据项目区分）
	PrjID int `json:"project_id"`
}

// SchemaID 实现接口TableCacher
func (ObjectRoute) SchemaID() string {
	return TBObjectRoute
}

// URL 实现接口TableCacher
func (o ObjectRoute) URL() string {
	return o.AccessUrl
}

// SearchFields 实现接口TableCacher
func (ObjectRoute) SearchFields() map[string]string {
	return map[string]string{TBObjectRoute: "dataset_id|object_id"}
}

// FilterKey 实现接口TableCacher
func (ObjectRoute) FilterKey() string {
	return "enabled=true"
}

// GetObjectRouteByID 获取指定对象的路由配置(返回存储实体ID)
func GetObjectRouteByID(datasetID int, objectID string) int {
	query := fmt.Sprintf("dataset_id=%d&object_id=%s", datasetID, objectID)
	route, ok := QueryDataItem(TBObjectRoute, query).(*ObjectRoute)
	if !ok {
		defaultQuery := fmt.Sprintf("dataset_id=%d&object_id=*", datasetID)
		defaultRoute, ok := QueryDataItem(TBObjectRoute, defaultQuery).(*ObjectRoute)
		if !ok {
			return 0
		}
		return defaultRoute.EntityID
	}
	return route.EntityID
}

// GetObjectRouteByDatasetAndObject 获取指定数据集和对象的路由配置
func GetObjectRouteByDatasetAndObject(datasetID int, objectID string) (*ObjectRoute, error) {
	query := fmt.Sprintf("dataset_id=%d&object_id=%s", datasetID, objectID)
	route, ok := QueryDataItem(TBObjectRoute, query).(*ObjectRoute)
	if !ok {
		// 尝试获取默认路由
		defaultQuery := fmt.Sprintf("dataset_id=%d&object_id=*", datasetID)
		defaultRoute, ok := QueryDataItem(TBObjectRoute, defaultQuery).(*ObjectRoute)
		if !ok {
			return nil, fmt.Errorf("未找到数据集[%d]对象[%s]的路由配置", datasetID, objectID)
		}
		return defaultRoute, nil
	}
	return route, nil
}

// GetObjectRoutesByDataset 获取指定数据集的所有路由配置
func GetObjectRoutesByDataset(datasetID int) ([]*ObjectRoute, error) {
	allRoutes, ok := GetAll(TBObjectRoute).([]*ObjectRoute)
	if !ok {
		return nil, fmt.Errorf("获取所有路由配置失败")
	}

	var result []*ObjectRoute
	for _, route := range allRoutes {
		if route.DatasetID == datasetID && route.Enabled == constants.EnabledValue {
			result = append(result, route)
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("数据集[%d]没有配置路由", datasetID)
	}
	return result, nil
}
