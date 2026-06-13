package dao

import (
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/sqlite"
	"gorm.io/gorm"
)

// DataInterfacer metadata数据层接口
type DataInterfacer interface {
	// InitMetadata 初始化元数据存储
	InitMetadata() error

	// ================================================================================
	// 项目管理接口

	// AddProject 添加项目
	AddProject(projID int, projName string, remark string) error
	// GetProjectByID 根据项目ID获取项目信息
	GetProjectByID(projID int) (*model.Project, error)
	// GetProjectList 获取项目列表
	GetProjectList() ([]model.Project, error)
	// UpdateProject 更新项目信息
	UpdateProject(projID int, projName string, remark string) error
	// DeleteProject 删除项目
	DeleteProject(projID int) error
	// GetMaxProjectID 获取当前最大的项目ID
	GetMaxProjectID() (int, error)

	// ================================================================================
	// 数据集管理接口

	// GetDatasetList 获取数据集列表
	GetDatasetList() ([]model.Dataset, error)
	// GetDatasetByID 根据数据集ID获取数据集信息
	GetDatasetByID(datasetID int) (*model.Dataset, error)
	// GetDatasetByProjID 根据项目ID获取数据集列表
	GetDatasetByProjID(projID int) ([]model.Dataset, error)
	// GetDatasetByDataType 根据数据类型获取数据集列表
	GetDatasetByDataType(dataType int) ([]model.Dataset, error)
	// AddDataset 添加数据集
	AddDataset(dataset *model.Dataset) error
	// UpdateDataset 更新数据集
	UpdateDataset(dataset *model.Dataset) error
	// DeleteDataset 删除数据集
	DeleteDataset(datasetID int) error
	// GetMaxDatasetIDInRange 获取指定范围内的最大数据集ID
	GetMaxDatasetIDInRange(minID, maxID int) (int, error)

	// ================================================================================
	// 字段管理接口

	// GetFieldItems 根据接口名获取字段列表
	GetFieldItems(interfaceName string) ([]model.Field, error)
	// GetValidFieldList 获取合法字段列表
	GetValidFieldList() ([]model.Field, error)
	// GetAllFieldList 获取所有字段信息，包括已删除的字段
	GetAllFieldList() ([]model.Field, error)
	// AddField 添加字段
	AddField(field *model.Field) error
	// UpdateField 更新字段
	UpdateField(field *model.Field) error
	// DeleteField 删除字段
	DeleteField(fieldID int) error
	// GetMaxFieldIDInRange 获取指定范围内的最大字段ID
	GetMaxFieldIDInRange(minID, maxID int) (int, error)

	// ================================================================================
	// 存储实体管理接口

	// GetEntityList 获取存储实体列表
	GetEntityList() ([]model.StorageEntity, error)
	// GetEntityByID 根据实体ID获取存储实体信息
	GetEntityByID(entityID int) (*model.StorageEntity, error)
	// AddEntity 添加存储实体
	AddEntity(entity *model.StorageEntity) error
	// UpdateEntity 更新存储实体
	UpdateEntity(entity *model.StorageEntity) error
	// DeleteEntity 删除存储实体
	DeleteEntity(entityID int) error
	// GetMaxEntityID 获取当前最大的存储实体ID
	GetMaxEntityID() (int, error)
	// IsEntityReferencedByObjectRoute 检查实体是否被数据对象路由引用
	IsEntityReferencedByObjectRoute(entityID int) (bool, error)

	// ================================================================================
	// 存储设备管理接口

	// GetDeviceList 获取存储设备列表
	GetDeviceList() ([]model.StorageDevice, error)
	// GetDeviceByID 根据设备ID获取存储设备信息
	GetDeviceByID(deviceID int) (*model.StorageDevice, error)
	// AddDevice 添加存储设备
	AddDevice(device *model.StorageDevice) error
	// UpdateDevice 更新存储设备
	UpdateDevice(device *model.StorageDevice) error
	// DeleteDevice 删除存储设备
	DeleteDevice(deviceID int) error
	// GetMaxDeviceID 获取当前最大的存储设备ID
	GetMaxDeviceID() (int, error)
	// IsDeviceReferencedByFieldRoute 检查设备是否被字段路由引用
	IsDeviceReferencedByFieldRoute(deviceID int) (bool, error)

	// ================================================================================
	// 数据对象路由管理接口

	// GetObjectRouteList 获取数据对象路由列表
	GetObjectRouteList() ([]model.ObjectRoute, error)
	// GetObjectRouteByID 根据路由ID获取数据对象路由信息
	GetObjectRouteByID(routeID int) (*model.ObjectRoute, error)
	// GetObjectRouteByDatasetID 根据数据集ID获取数据对象路由列表
	GetObjectRouteByDatasetID(datasetID int) ([]model.ObjectRoute, error)
	// GetObjectRouteByEntityID 根据实体ID获取数据对象路由列表
	GetObjectRouteByEntityID(entityID int) ([]model.ObjectRoute, error)
	// GetObjectRouteListWithFilter 支持分页和过滤的数据对象路由查询
	GetObjectRouteListWithFilter(projectID, datasetID, entityID int, limit, offset int) ([]model.ObjectRoute, int, error)
	// AddObjectRoute 添加数据对象路由
	AddObjectRoute(route *model.ObjectRoute) error
	// UpdateObjectRoute 更新数据对象路由
	UpdateObjectRoute(route *model.ObjectRoute) error
	// DeleteObjectRoute 删除数据对象路由
	DeleteObjectRoute(routeID int) error

	// ================================================================================
	// 数据字段路由管理接口

	// GetFieldRouteList 获取数据字段路由列表
	GetFieldRouteList() ([]model.FieldRoute, error)
	// GetFieldRouteByID 根据路由ID获取数据字段路由信息
	GetFieldRouteByID(routeID int) (*model.FieldRoute, error)
	// GetFieldRouteByFieldID 根据字段ID获取数据字段路由列表
	GetFieldRouteByFieldID(fieldID int) ([]model.FieldRoute, error)
	// GetFieldRouteByDeviceID 根据设备ID获取数据字段路由列表
	GetFieldRouteByDeviceID(deviceID int) ([]model.FieldRoute, error)
	// GetFieldRouteListWithFilter 支持分页和过滤的数据字段路由查询
	GetFieldRouteListWithFilter(projectID, fieldID, dataCategory, deviceID int, limit, offset int) ([]model.FieldRoute, int, error)
	// AddFieldRoute 添加数据字段路由
	AddFieldRoute(route *model.FieldRoute) error
	// UpdateFieldRoute 更新数据字段路由
	UpdateFieldRoute(route *model.FieldRoute) error
	// DeleteFieldRoute 删除数据字段路由
	DeleteFieldRoute(routeID int) error

	// ================================================================================
	// 字段列名映射管理接口

	// GetColumnMapList 获取所有字段列名映射列表
	GetColumnMapList() ([]model.FieldColumnMap, error)
	// GetColumnMapByFieldID 根据字段ID获取列名映射
	GetColumnMapByFieldID(fieldID int) ([]model.FieldColumnMap, error)
	// GetColumnMapByProjectID 根据项目ID获取列名映射
	GetColumnMapByProjectID(projectID int) ([]model.FieldColumnMap, error)
	// GetColumnMapByProjectAndType 根据项目ID和表类型获取列名映射
	GetColumnMapByProjectAndType(projectID, tableType int) ([]model.FieldColumnMap, error)
	// GetColumnMapByFieldProjectAndType 根据字段ID、项目ID和表类型获取列名映射
	GetColumnMapByFieldProjectAndType(fieldID, projectID, tableType int) (*model.FieldColumnMap, error)
	// AddColumnMap 添加新的字段列名映射
	AddColumnMap(column *model.FieldColumnMap) error
	// UpdateColumnMap 更新字段列名映射
	UpdateColumnMap(column *model.FieldColumnMap) error
	// DeleteColumnMap 删除字段列名映射（物理删除）
	DeleteColumnMap(id int) error
	// DeleteColumnMapByFieldID 根据字段ID删除所有相关的列名映射
	DeleteColumnMapByFieldID(fieldID int) error
	// DeleteColumnMapByProjectID 根据项目ID删除所有相关的列名映射
	DeleteColumnMapByProjectID(projectID int) error
	// DeleteColumnMapByProjectAndType 根据项目ID和表类型删除所有相关的列名映射
	DeleteColumnMapByProjectAndType(projectID, tableType int) error
	// BatchAddColumnMaps 批量添加字段列名映射
	BatchAddColumnMaps(columns []model.FieldColumnMap) error
	// GetColumnNameByFieldProjectAndType 根据字段ID、项目ID和表类型获取列名
	GetColumnNameByFieldProjectAndType(fieldID, projectID, tableType int) (string, error)
	// GetFieldIDsByProjectTypeAndColumn 根据项目ID、表类型和列名获取字段ID列表
	GetFieldIDsByProjectTypeAndColumn(projectID, tableType int, columnName string) ([]int, error)
	// IsColumnMapExists 检查字段列名映射是否存在
	IsColumnMapExists(fieldID, projectID, tableType int) (bool, error)

	// ================================================================================
	// 事务管理接口

	// BeginTx 开始事务
	BeginTx() (*gorm.DB, error)
	// CommitTx 提交事务
	CommitTx(tx *gorm.DB) error
	// RollbackTx 回滚事务
	RollbackTx(tx *gorm.DB) error
	// AddProjectWithTx 在事务中添加项目
	AddProjectWithTx(tx *gorm.DB, projID int, projName string, remark string) error
	// AddDatasetWithTx 在事务中添加数据集
	AddDatasetWithTx(tx *gorm.DB, dataset *model.Dataset) error
	// AddFieldWithTx 在事务中添加字段
	AddFieldWithTx(tx *gorm.DB, field *model.Field) error
}

// NewDataInterfacer 新建metadata数据层
func NewDataInterfacer() (DataInterfacer, error) {
	dataInterfacerOnce.Do(func() {
		dataInterfacer, dataInterfacerErr = sqlite.InitSQLiteImp()
	})
	return dataInterfacer, dataInterfacerErr
}

var (
	// Reuse a single DAO instance to avoid repeated DB opens from timers/handlers.
	dataInterfacerOnce sync.Once
	dataInterfacer     DataInterfacer
	dataInterfacerErr  error
)
