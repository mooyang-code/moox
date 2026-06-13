package logic

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	trpcErrs "trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

// getFieldRoutes 获取并验证字段路由信息
func getFieldRoutes(ctx context.Context) ([]*cache.FieldRoute, error) {
	routes := cache.GetAll(cache.TBFieldRoute)
	if routes == nil {
		log.ErrorContextf(ctx, "获取字段路由失败")
		return nil, trpcErrs.New(int(pb.EnumErrorCode_FAILED_SELECT), "获取字段路由失败")
	}

	fieldRoutes, ok := routes.([]*cache.FieldRoute)
	if !ok {
		log.ErrorContextf(ctx, "字段路由类型转换失败")
		return nil, trpcErrs.New(int(pb.EnumErrorCode_INNER_ERR), "字段路由类型转换失败")
	}
	return fieldRoutes, nil
}

// BuildFieldMap 根据路由信息构建设备到字段的映射
// 路由优先级（t_field_route表）：
//   - 高优先级：特定字段配置 (字段ID为具体值，不等于999999999)
//   - 中优先级：特定数据集的通用配置 (字段ID=999999999, 数据集ID为具体值)
//   - 低优先级：所有字段所有数据集 (字段ID=999999999, 数据集ID=0)
//
// 注意：字段路由配置现在对所有存储实体都生效，不再区分entityID
// 用户必须使用999999999来表示"所有字段"，使用0表示"所有数据集"
func BuildFieldMap(ctx context.Context, fieldIDs []uint32,
	datasetID int) (map[int][]uint32, []uint32, error) {
	// 获取并验证字段路由信息
	fieldRoutes, err := getFieldRoutes(ctx)
	if err != nil {
		return nil, nil, err
	}

	// 初始化优先级映射
	maps := &PriorityMaps{
		HighPriorityFieldMap:  make(map[uint32]int), // 特定字段（key为字段ID）
		MediumPriorityDevices: make(map[int]bool),   // 数据集配置（key为设备ID）
		AllDevices:            make(map[int]bool),   // 所有设备
	}

	// 处理路由信息
	processRoutes(fieldRoutes, datasetID, maps)

	// 处理空字段ID列表的情况
	if len(fieldIDs) == 0 {
		deviceFieldMap := make(map[int][]uint32)
		for deviceID := range maps.AllDevices {
			deviceFieldMap[deviceID] = []uint32{} // 空字段列表，表示获取设备上的所有字段
			log.DebugContextf(ctx, "添加设备[%d]到映射", deviceID)
		}
		return deviceFieldMap, nil, nil
	}

	// 按优先级规则分配字段到设备
	deviceFieldMap, unroutedFields := assignFieldsByPriority(ctx, fieldIDs, maps.HighPriorityFieldMap, maps.MediumPriorityDevices)
	return deviceFieldMap, unroutedFields, nil
}

// PriorityMaps 优先级映射集合
type PriorityMaps struct {
	HighPriorityFieldMap  map[uint32]int // 特定字段映射
	MediumPriorityDevices map[int]bool   // 数据集配置设备
	AllDevices            map[int]bool   // 所有设备
}

// processRoutes 处理路由信息，填充优先级映射
func processRoutes(fieldRoutes []*cache.FieldRoute, datasetID int, maps *PriorityMaps) {
	for _, route := range fieldRoutes {
		if route.DatasetID != datasetID { // 只处理匹配的数据集
			continue
		}
		maps.AllDevices[route.DeviceID] = true
		if route.FieldID == constants.AllFieldsMarker {
			// 特定数据集的通用配置（优先级中）
			maps.MediumPriorityDevices[route.DeviceID] = true
		}

		// 特定字段的配置（优先级最高），我们把默认配置标识的字段ID也放入进去，这样请求方可以获取默认配置
		maps.HighPriorityFieldMap[uint32(route.FieldID)] = route.DeviceID
	}
}

// assignFieldsByPriority 按优先级规则分配字段到设备
func assignFieldsByPriority(ctx context.Context, fieldIDs []uint32,
	highPriorityFieldMap map[uint32]int, mediumPriorityDevices map[int]bool,
) (map[int][]uint32, []uint32) {
	deviceFieldMap := make(map[int][]uint32)
	var unroutedFields []uint32

	for _, fieldID := range fieldIDs {
		// 首先检查是否有高优先级配置（特定字段）
		if deviceID, exists := highPriorityFieldMap[fieldID]; exists {
			deviceFieldMap[deviceID] = append(deviceFieldMap[deviceID], fieldID)
			log.DebugContextf(ctx, "字段[%d]使用高优先级设备[%d]", fieldID, deviceID)
			continue
		}

		// 然后检查是否有中优先级的数据集配置
		var foundDevice bool
		for deviceID := range mediumPriorityDevices {
			deviceFieldMap[deviceID] = append(deviceFieldMap[deviceID], fieldID)
			log.DebugContextf(ctx, "字段[%d]使用数据集配置设备[%d]", fieldID, deviceID)
			foundDevice = true
			break // 只使用一个设备
		}

		// 如果没有找到任何可用设备，记录为无法路由的字段
		if !foundDevice {
			unroutedFields = append(unroutedFields, fieldID)
			log.WarnContextf(ctx, "字段[%d]没有找到匹配的存储设备", fieldID)
		}
	}
	return deviceFieldMap, unroutedFields
}

// ValidateDataType 验证数据类型是否有效
func ValidateDataType(dataType pb.EnumDataTypeCategory) bool {
	return dataType == pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE ||
		dataType == pb.EnumDataTypeCategory_STATIC_DATA_TYPE
}
