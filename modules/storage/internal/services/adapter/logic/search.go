package logic

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	trpcErrs "trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

// SearchFieldInfos 从存储设备搜索字段数据
// 该函数实现了单设备的字段搜索能力，支持时序数据和静态数据两种类型
// 主要步骤：
// 1. 验证搜索条件及数据类型合法性
// 2. 构建"字段-设备"映射关系，确定每个字段的存储位置
// 3. 验证所有字段必须在同一设备上（不支持跨设备检索）
// 4. 执行单设备搜索并返回结果
//
// 参数：
//   - ctx: 上下文信息，用于传递超时、跟踪等信息
//   - req: 搜索请求参数，包含数据类型、实体ID、表ID、搜索条件等
//
// 返回值：
//   - 搜索结果，包含字段数据和错误信息
//   - 执行错误
//
// 错误处理：
//   - 对搜索条件中的非法字段，直接返回错误
//   - 对返回字段和排序字段中的非法字段，进行柔性处理（记录错误但继续执行）
//   - 若发现字段分布在不同设备，直接报错
func (a *AdapterImpl) SearchFieldInfos(ctx context.Context, req *pb.SearchFieldInfosReq) (*pb.SearchFieldInfosRsp, error) {
	log.DebugContextf(ctx, "####### Adapter SearchFieldInfos : %+v #######", req)
	// 1. 初始化响应对象
	rsp := &pb.SearchFieldInfosRsp{
		RetInfo:      &pb.RetInfo{},
		FailedFields: make(map[uint32]*pb.FailedInfo),
	}

	// 2. 验证搜索条件
	if req.SearchOptions == nil || len(req.SearchOptions.CondGroups) == 0 {
		log.DebugContextf(ctx, "搜索条件为空，将返回全部数据")
	}

	// 3. 准备参数及字段与设备映射关系（包含设备一致性检查）
	params, err := prepareSearchFieldInfos(ctx, req)
	if err != nil {
		return rsp, err
	}
	if err := a.handlePreparationErrors(params, rsp); err != nil {
		return rsp, err
	}

	// 4. 验证设备可用性
	if len(params.DeviceSearchFieldMap) == 0 {
		log.ErrorContextf(ctx, "没有可用的存储设备配置")
		rsp.RetInfo.Code = pb.EnumErrorCode_NO_ROUTE_STORE_ITEM
		rsp.RetInfo.Msg = "没有配置存储设备"
		return rsp, nil
	}

	// 5. 执行单设备搜索（因为已经验证所有字段都在同一设备上）
	docRows, total, err := a.executeSingleDeviceSearch(ctx, req, params)
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_SELECT
		rsp.RetInfo.Msg = fmt.Sprintf("搜索数据失败: %v", err)
		return rsp, err
	}

	// 6. 返回结果
	rsp.DocRows = docRows
	rsp.Total = total
	return rsp, nil
}

// executeSingleDeviceSearch 执行单设备搜索
func (a *AdapterImpl) executeSingleDeviceSearch(ctx context.Context, req *pb.SearchFieldInfosReq, params *prepareSearchFieldInfosParams) ([]*pb.DocRow, uint64, error) {
	// 获取设备ID（由于已经验证所有字段都在同一设备上，只需要取第一个设备）
	var deviceID int
	var searchFieldIDs []uint32

	// 从搜索字段映射中获取设备ID
	for id, fieldIDs := range params.DeviceSearchFieldMap {
		deviceID = id
		searchFieldIDs = fieldIDs
		break
	}

	// 如果没有搜索字段，从返回字段或排序字段中获取设备ID
	if deviceID == 0 {
		for id := range params.DeviceReturnMap {
			deviceID = id
			break
		}
		if deviceID == 0 {
			for id := range params.DeviceOrderFieldMap {
				deviceID = id
				break
			}
		}
	}
	if deviceID == 0 {
		return nil, 0, fmt.Errorf("未找到有效的存储设备")
	}

	// 获取该设备需要返回的字段
	var returnFieldIDs []uint32
	if deviceReturnFields, ok := params.DeviceReturnMap[deviceID]; ok {
		returnFieldIDs = deviceReturnFields
	}

	// 获取该设备需要排序的字段
	var orderFieldIDs []uint32
	if deviceOrderFields, ok := params.DeviceOrderFieldMap[deviceID]; ok {
		orderFieldIDs = deviceOrderFields
	}

	// 构建搜索参数
	var searchParams *dao.SearchFieldParams
	hasSearchConditions := req.GetSearchOptions() != nil && len(req.GetSearchOptions().CondGroups) > 0

	if !hasSearchConditions {
		// 没有搜索条件的场景
		searchParams = createSearchParamsForNoConditions(req, params, returnFieldIDs)
	} else {
		// 有搜索条件的场景
		searchParams = createSearchParamsWithConditions(req, params, searchFieldIDs, returnFieldIDs, orderFieldIDs)
	}

	log.DebugContextf(ctx, "执行单设备搜索，设备ID: %d", deviceID)

	// 执行设备搜索
	return executeDeviceSearch(ctx, deviceID, searchParams)
}

// handlePreparationErrors 处理准备阶段的错误
func (a *AdapterImpl) handlePreparationErrors(params *prepareSearchFieldInfosParams, rsp *pb.SearchFieldInfosRsp) error {
	if params.RetInfo.Code != 0 {
		rsp.RetInfo.Code = params.RetInfo.Code
		rsp.RetInfo.Msg = params.RetInfo.Msg
		return nil
	}

	// 将准备阶段失败的字段添加到响应中
	for fieldID, errMsg := range params.FailedSearchFields {
		rsp.FailedFields[fieldID] = &pb.FailedInfo{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errMsg,
		}
	}
	for fieldID, errMsg := range params.FailedReturnFields {
		rsp.FailedFields[fieldID] = &pb.FailedInfo{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errMsg,
		}
	}
	return nil
}

// 搜索参数结构
type prepareSearchFieldInfosParams struct {
	DeviceSearchFieldMap map[int][]uint32        // 存储设备ID到搜索字段ID的映射
	DeviceReturnMap      map[int][]uint32        // 存储设备ID到返回字段ID的映射
	DeviceOrderFieldMap  map[int][]uint32        // 存储设备ID到排序字段ID的映射
	DataType             pb.EnumDataTypeCategory // 数据类型
	RetInfo              *pb.RetInfo             // 返回信息
	FailedSearchFields   map[uint32]string       // 存储失败的字段ID及原因
	FailedReturnFields   map[uint32]string       // 存储失败的返回字段ID及原因
}

// prepareSearchFieldInfos 准备搜索字段信息的参数
func prepareSearchFieldInfos(ctx context.Context, req *pb.SearchFieldInfosReq) (*prepareSearchFieldInfosParams, error) {
	// 初始化结果结构
	result := &prepareSearchFieldInfosParams{
		RetInfo:              &pb.RetInfo{},
		DeviceSearchFieldMap: make(map[int][]uint32),
		DeviceReturnMap:      make(map[int][]uint32),
		DeviceOrderFieldMap:  make(map[int][]uint32),
		FailedSearchFields:   make(map[uint32]string),
		FailedReturnFields:   make(map[uint32]string),
	}

	// 1. 验证数据类型
	if err := validateDataType(ctx, req, result); err != nil {
		return result, nil
	}

	// 2. 收集所有相关字段ID并构建设备映射
	if err := buildDeviceMap(ctx, req, result); err != nil {
		return result, err
	}
	return result, nil
}

// validateDataType 验证数据类型
func validateDataType(ctx context.Context, req *pb.SearchFieldInfosReq, result *prepareSearchFieldInfosParams) error {
	result.DataType = req.GetDataType()
	if result.DataType != pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE &&
		result.DataType != pb.EnumDataTypeCategory_STATIC_DATA_TYPE {
		log.WarnContextf(ctx, "未知的数据类型: %d", result.DataType)
		result.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		result.RetInfo.Msg = fmt.Sprintf("未知的数据类型: %d", result.DataType)
		return fmt.Errorf("未知的数据类型")
	}
	return nil
}

// buildDeviceMap 收集所有相关字段ID并构建设备映射
func buildDeviceMap(ctx context.Context, req *pb.SearchFieldInfosReq, result *prepareSearchFieldInfosParams) error {
	// 1. 一次性提取所有相关字段ID
	searchFieldIDs, returnFieldIDs, orderFieldIDs, allFieldIDs := extractAllFieldIDs(req.SearchOptions)

	// 2. 从表ID中解析数据集ID
	datasetID, err := utils.ParseDatasetIDFromTableID(req.GetTableId())
	if err != nil {
		log.WarnContextf(ctx, "从表ID解析数据集ID失败: %v", err)
		result.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		result.RetInfo.Msg = fmt.Sprintf("无效的表ID格式: %v", err)
		return fmt.Errorf("无效的表ID格式")
	}

	// 3. 如果没有任何字段，使用默认全部字段配置（AllFieldsMarker）
	if len(allFieldIDs) == 0 {
		log.DebugContextf(ctx, "没有搜索条件，使用数据集的默认配置")
		defaultDeviceMap, _, err := BuildFieldMap(ctx, []uint32{uint32(constants.AllFieldsMarker)}, int(datasetID))
		if err != nil {
			log.ErrorContextf(ctx, "获取数据集默认配置失败: %v", err)
			return err
		}
		result.DeviceSearchFieldMap = defaultDeviceMap
		return nil
	}

	// 4. 一次性构建所有字段的设备映射
	deviceFieldMap, unroutedFields, err := BuildFieldMap(ctx, allFieldIDs, int(datasetID))
	if err != nil {
		return err
	}

	// 5. 处理无法路由的字段
	for _, fieldID := range unroutedFields {
		// 判断是搜索字段还是其他字段，搜索字段直接报错，其他字段柔性处理
		isSearchField := false
		for _, searchFieldID := range searchFieldIDs {
			if fieldID == searchFieldID {
				isSearchField = true
				break
			}
		}

		if isSearchField {
			log.WarnContextf(ctx, "搜索条件中包含无法路由的字段: %d", fieldID)
			result.RetInfo.Code = pb.EnumErrorCode_NO_ROUTE_STORE_ITEM
			result.RetInfo.Msg = fmt.Sprintf("搜索条件中包含无法路由的字段: %d", fieldID)
			return fmt.Errorf("存在无法路由的字段")
		} else {
			// 返回字段或排序字段的柔性处理
			log.WarnContextf(ctx, "返回字段或排序字段中包含无法路由的字段: %d", fieldID)
			result.FailedReturnFields[fieldID] = "没有找到匹配的存储设备"
		}
	}

	// 6. 根据字段类型分组到对应的设备映射中
	result.DeviceSearchFieldMap = filterDeviceMapByFields(deviceFieldMap, searchFieldIDs)
	result.DeviceReturnMap = filterDeviceMapByFields(deviceFieldMap, returnFieldIDs)
	result.DeviceOrderFieldMap = filterDeviceMapByFields(deviceFieldMap, orderFieldIDs)

	// 7. 验证单设备约束
	return validateSingleDeviceConstraint(ctx, result)
}

// filterDeviceMapByFields 根据字段ID列表过滤设备映射
func filterDeviceMapByFields(deviceFieldMap map[int][]uint32, targetFieldIDs []uint32) map[int][]uint32 {
	if len(targetFieldIDs) == 0 {
		return make(map[int][]uint32)
	}

	targetFieldSet := make(map[uint32]bool)
	for _, fieldID := range targetFieldIDs {
		targetFieldSet[fieldID] = true
	}

	result := make(map[int][]uint32)
	for deviceID, fieldIDs := range deviceFieldMap {
		var filteredFields []uint32
		for _, fieldID := range fieldIDs {
			if targetFieldSet[fieldID] {
				filteredFields = append(filteredFields, fieldID)
			}
		}
		if len(filteredFields) > 0 {
			result[deviceID] = filteredFields
		}
	}
	return result
}

// extractAllFieldIDs 从搜索选项中提取所有相关字段ID
// 返回值：搜索字段ID列表、返回字段ID列表、排序字段ID列表、去重后的全部字段ID列表
func extractAllFieldIDs(options *pb.SearchOptions) ([]uint32, []uint32, []uint32, []uint32) {
	var searchFieldIDs []uint32
	var returnFieldIDs []uint32
	var orderFieldIDs []uint32

	if options == nil {
		return searchFieldIDs, returnFieldIDs, orderFieldIDs, []uint32{}
	}

	// 提取搜索字段ID
	if len(options.CondGroups) > 0 {
		for _, group := range options.CondGroups {
			for _, cond := range group.Conds {
				searchFieldIDs = append(searchFieldIDs, cond.FieldId)
			}
		}
	}

	// 提取返回字段ID
	if len(options.ReturnFieldIds) > 0 {
		returnFieldIDs = options.ReturnFieldIds
	}

	// 提取排序字段ID
	if len(options.Sort) > 0 {
		for _, sort := range options.Sort {
			orderFieldIDs = append(orderFieldIDs, sort.FieldId)
		}
	}

	// 合并所有字段ID并去重
	allFieldIDMap := make(map[uint32]bool)
	for _, fieldID := range searchFieldIDs {
		allFieldIDMap[fieldID] = true
	}
	for _, fieldID := range returnFieldIDs {
		allFieldIDMap[fieldID] = true
	}
	for _, fieldID := range orderFieldIDs {
		allFieldIDMap[fieldID] = true
	}

	var allFieldIDs []uint32
	for fieldID := range allFieldIDMap {
		allFieldIDs = append(allFieldIDs, fieldID)
	}
	return searchFieldIDs, returnFieldIDs, orderFieldIDs, allFieldIDs
}

// validateSingleDeviceConstraint 验证所有字段都在同一设备上
func validateSingleDeviceConstraint(ctx context.Context, result *prepareSearchFieldInfosParams) error {
	// 收集所有涉及的设备ID
	deviceIDSet := make(map[int]bool)

	// 添加搜索字段对应的设备
	for deviceID := range result.DeviceSearchFieldMap {
		deviceIDSet[deviceID] = true
	}

	// 添加返回字段对应的设备
	for deviceID := range result.DeviceReturnMap {
		deviceIDSet[deviceID] = true
	}

	// 添加排序字段对应的设备
	for deviceID := range result.DeviceOrderFieldMap {
		deviceIDSet[deviceID] = true
	}

	// 检查是否超过一个设备
	if len(deviceIDSet) > 1 {
		deviceIDs := make([]int, 0, len(deviceIDSet))
		for deviceID := range deviceIDSet {
			deviceIDs = append(deviceIDs, deviceID)
		}
		log.ErrorContextf(ctx, "检索涉及多个设备，不支持跨设备检索: %v", deviceIDs)
		result.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		result.RetInfo.Msg = fmt.Sprintf("检索字段分布在多个设备上(%v)，不支持跨设备检索", deviceIDs)
		return fmt.Errorf("不支持跨设备检索")
	}

	log.DebugContextf(ctx, "所有字段都在同一设备上，检查通过")
	return nil
}

// createSearchParamsForNoConditions 为无搜索条件场景创建搜索参数
func createSearchParamsForNoConditions(req *pb.SearchFieldInfosReq, params *prepareSearchFieldInfosParams,
	returnFieldIDs []uint32) *dao.SearchFieldParams {
	var maxNum uint32
	if req.GetSearchOptions() != nil {
		maxNum = req.GetSearchOptions().GetMaxNum()
	}
	return &dao.SearchFieldParams{
		TableID:      utils.EscapeTableIDDash(req.GetTableId()),
		DataType:     params.DataType,
		TimeInterval: req.GetTimeInterval(),
		TimeSort:     req.GetTimeSort(), // 传递时序排序参数
		RowID:        req.GetRowId(),
		SearchOptions: &pb.SearchOptions{
			Logical:        pb.Logical_LogicalAnd,
			ReturnFieldIds: returnFieldIDs,
			MaxNum:         maxNum,
		},
		PageInfo: req.GetPageInfo(),
	}
}

// createSearchParamsWithConditions 为有搜索条件场景创建搜索参数
func createSearchParamsWithConditions(req *pb.SearchFieldInfosReq, params *prepareSearchFieldInfosParams,
	searchFieldIDs, returnFieldIDs, orderFieldIDs []uint32) *dao.SearchFieldParams {
	// 根据字段ID列表，从原始搜索选项中筛选出该设备对应的条件
	deviceSearchOptions := filterSearchOptionsByFieldIDs(req.GetSearchOptions(), searchFieldIDs, orderFieldIDs)

	// 设置返回字段ID列表
	if deviceSearchOptions != nil && len(returnFieldIDs) > 0 {
		deviceSearchOptions.ReturnFieldIds = returnFieldIDs
	}

	return &dao.SearchFieldParams{
		TableID:       utils.EscapeTableIDDash(req.GetTableId()),
		DataType:      params.DataType,
		TimeInterval:  req.GetTimeInterval(),
		TimeSort:      req.GetTimeSort(), // 传递时序排序参数
		RowID:         req.GetRowId(),
		SearchOptions: deviceSearchOptions,
		PageInfo:      req.GetPageInfo(),
	}
}

// filterSearchOptionsByFieldIDs 根据字段ID列表筛选搜索选项
func filterSearchOptionsByFieldIDs(options *pb.SearchOptions, searchFieldIDs []uint32, orderFieldIDs []uint32) *pb.SearchOptions {
	if options == nil {
		return nil
	}

	// 创建字段ID集合，用于快速查找
	searchFieldIDSet := make(map[uint32]bool)
	for _, id := range searchFieldIDs {
		searchFieldIDSet[id] = true
	}

	// 创建排序字段ID集合
	orderFieldIDSet := make(map[uint32]bool)
	for _, id := range orderFieldIDs {
		orderFieldIDSet[id] = true
	}

	// 创建新的搜索选项
	newOptions := &pb.SearchOptions{
		Logical: options.Logical,
		MaxNum:  options.MaxNum,
	}

	// 筛选条件组(从原始条件组中挑出本字段的搜索条件)
	for _, group := range options.CondGroups {
		newGroup := &pb.SearchCondGroup{
			Logical: group.Logical,
		}

		// 筛选符合字段ID的条件
		for _, cond := range group.Conds {
			if searchFieldIDSet[cond.FieldId] {
				newGroup.Conds = append(newGroup.Conds, cond)
			}
		}

		// 只有当有条件时才添加条件组
		if len(newGroup.Conds) > 0 {
			newOptions.CondGroups = append(newOptions.CondGroups, newGroup)
		}
	}

	// 筛选排序条件
	for _, sort := range options.Sort {
		if orderFieldIDSet[sort.FieldId] {
			newOptions.Sort = append(newOptions.Sort, sort)
		}
	}
	return newOptions
}

// executeDeviceSearch 从指定设备搜索数据
func executeDeviceSearch(ctx context.Context, deviceID int, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
	// 创建设备对象
	storeDevice, err := dao.NewStoreDevice(ctx, deviceID)
	if err != nil {
		log.ErrorContextf(ctx, "创建存储设备失败[%d], err:%v", deviceID, err)
		return nil, 0, trpcErrs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("创建存储设备失败: %v", err))
	}

	// 调用设备对象的SearchFieldInfos函数搜索数据
	docRows, total, err := storeDevice.SearchFieldInfos(ctx, params)
	if err != nil {
		log.ErrorContextf(ctx, "executeDeviceSearch 设备[%d]搜索数据失败: %v", deviceID, err)
		return nil, 0, err
	}
	return docRows, total, nil
}
