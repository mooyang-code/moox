package logic

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// SearchFieldInfosReqWrap 检索接口请求包装器
type SearchFieldInfosReqWrap struct {
	*pb.SearchFieldInfosReq
	EntityId uint32
}

// SearchData 实现条件查询数据接口
func (i *accessorImpl) SearchData(ctx context.Context, req *pb.SearchDataReq) (*pb.SearchDataRsp, error) {
	log.DebugContextf(ctx, "Accessor SearchData: req=%v", req)
	// 调试：打印接收到的时间范围
	if req.TimeRange != nil {
		log.DebugContextf(ctx, "DEBUG: 接收到的时间范围 - Start: %s, End: %s", req.TimeRange.Start, req.TimeRange.GetEnd())
	}
	// 1. 校验参数
	if err := validateSearchDataParams(req); err != nil {
		log.ErrorContextf(ctx, "SearchData: 参数校验失败: %v", err)
		return genSearchDataRsp(pb.EnumErrorCode_INVALID_PARAM, err.Error()), nil
	}

	// 2. 准备适配层请求参数
	adapterReqWrap, err := i.prepareSearchDataAdapterReq(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "SearchData: 准备适配层请求参数失败: %v", err)
		return genSearchDataRsp(pb.EnumErrorCode_INNER_ERR, err.Error()), nil
	}

	// 3. 调用适配层服务
	// 创建动态适配层客户端
	adapterClient := CreateDynamicAdapterClient(int(adapterReqWrap.EntityId))

	// 调用适配层服务
	adapterRsp, err := adapterClient.SearchFieldInfos(ctx, adapterReqWrap.SearchFieldInfosReq)
	if err != nil {
		log.ErrorContextf(ctx, "SearchData: 调用适配层服务失败: %v", err)
		return genSearchDataRsp(pb.EnumErrorCode_INNER_ERR, fmt.Sprintf("调用适配层服务失败: %v", err)), nil
	}

	// 4. 检查适配层响应状态
	if adapterRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
		log.ErrorContextf(ctx, "SearchData: 适配层返回错误: code=%v, msg=%v",
			adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg)
		return genSearchDataRsp(adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg), nil
	}

	// 5. 获取字段ID到字段名的映射
	fieldID2Name := cache.BuildFieldID2NameMapping(req.DataKey.ProjectId)

	// 6. 转换结果
	var dataRows []*pb.DataRow
	for _, docRow := range adapterRsp.DocRows {
		dataRow := convertDocRow2DataRow(docRow, fieldID2Name)
		dataRows = append(dataRows, dataRow)
	}

	// 7. 转换失败字段信息
	failedFields := make(map[string]*pb.FailedInfo)
	for fieldID, failedInfo := range adapterRsp.FailedFields {
		fieldName, ok := fieldID2Name[fieldID]
		if !ok {
			fieldName = fmt.Sprintf("field_%d", fieldID)
		}
		failedFields[fieldName] = failedInfo
	}

	// 7. 组装返回结果
	return &pb.SearchDataRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		Total:        adapterRsp.Total,
		DataRows:     dataRows,
		FailedFields: failedFields,
	}, nil
}

// validateSearchDataParams 验证请求参数
func validateSearchDataParams(req *pb.SearchDataReq) error {
	if req.AuthInfo == nil {
		return fmt.Errorf("auth_info is nil")
	}
	if req.DataKey == nil {
		return fmt.Errorf("data_key is nil")
	}
	if req.DataKey.ProjectId <= 0 {
		return fmt.Errorf("invalid project_id: %d", req.DataKey.ProjectId)
	}
	if req.DataKey.DatasetId <= 0 {
		return fmt.Errorf("invalid dataset_id: %d", req.DataKey.DatasetId)
	}
	if req.DataKey.ObjectId == "" {
		return fmt.Errorf("empty object_id")
	}
	// 校验ObjectID格式
	if err := ValidateObjectID(req.DataKey.ObjectId); err != nil {
		return fmt.Errorf("invalid object_id: %v", err)
	}
	// 校验频率格式，允许为空
	if req.DataKey.Freq != "" {
		if err := ValidateFreq(req.DataKey.Freq); err != nil {
			return fmt.Errorf("invalid freq: %v", err)
		}
	}
	return nil
}

// prepareSearchDataAdapterReq 准备适配层请求
func (i *accessorImpl) prepareSearchDataAdapterReq(ctx context.Context, req *pb.SearchDataReq) (*SearchFieldInfosReqWrap, error) {
	// 1. 生成表ID
	tableID := utils.GenDataTableID(req.DataKey.DatasetId, req.DataKey.ObjectId, req.DataKey.Freq)
	log.DebugContextf(ctx, "使用表ID: %s", tableID)

	// 2. 获取数据集的存储路由
	entityID, err := GetDataRoute(req.DataKey.DatasetId, req.DataKey.ObjectId)
	if err != nil {
		return nil, err
	}

	// 3. 获取字段名到字段ID的映射
	fieldName2ID := cache.BuildFieldName2IDMapping(req.DataKey.ProjectId)

	// 4. 确定 rowID
	// 对于时序数据，使用 object_id 作为 rowID（便于数据隔离和高效查询）
	// 如果用户显式指定了 rowId，则使用用户指定的值
	rowID := req.RowId
	if rowID == "" && getDataTypeFromDataset(int(req.DataKey.DatasetId)) == pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE {
		rowID = req.DataKey.ObjectId
		log.DebugContextf(ctx, "时序数据查询：使用 object_id 作为 rowID: %s", rowID)
	}

	// 5. 构建适配层请求
	adapterReq := &SearchFieldInfosReqWrap{
		SearchFieldInfosReq: &pb.SearchFieldInfosReq{
			TableId:       tableID,
			DataType:      getDataTypeFromDataset(int(req.DataKey.DatasetId)),
			TimeInterval:  BuildTimeInterval(ctx, req.TimeRange, req.DataKey.Freq),
			TimeSort:      req.TimeSort, // 传递时序排序参数
			RowId:         rowID,
			SearchOptions: convertOptions2SearchOptions(req.Options, fieldName2ID),
			PageInfo:      req.PageInfo,
		},
		EntityId: uint32(entityID),
	}

	// 6. 打印转换后的搜索选项摘要
	if adapterReq.SearchFieldInfosReq.SearchOptions != nil {
		so := adapterReq.SearchFieldInfosReq.SearchOptions
		log.DebugContextf(ctx, "搜索选项摘要: 条件组数=%d, 组间逻辑=%v, 排序字段数=%d, 返回最大数量=%d",
			len(so.CondGroups), so.Logical, len(so.Sort), so.MaxNum)
	}
	return adapterReq, nil
}

// 生成错误响应
func genSearchDataRsp(code pb.EnumErrorCode, msg string) *pb.SearchDataRsp {
	return &pb.SearchDataRsp{
		RetInfo: &pb.RetInfo{
			Code: code,
			Msg:  msg,
		},
		DataRows:     make([]*pb.DataRow, 0),
		FailedFields: make(map[string]*pb.FailedInfo),
	}
}

// 转换时间范围（已弃用，使用BuildTimeInterval替代）
func convertTimeRange(timeRange *pb.TimeRange) *pb.TimeInterval {
	if timeRange == nil {
		return nil
	}
	return &pb.TimeInterval{
		Start: timeRange.GetStart(),
		End:   timeRange.GetEnd(),
	}
}

// 将DocRow转换为DataRow
func convertDocRow2DataRow(docRow *pb.DocRow, fieldID2Name map[uint32]string) *pb.DataRow {
	dataRow := &pb.DataRow{
		RowId:  docRow.RowId,
		Times:  docRow.Times,
		Fields: make(map[string]*pb.FieldValue),
	}

	// 转换字段
	for fieldID, fieldInfo := range docRow.Fields {
		fieldName, ok := fieldID2Name[fieldID]
		if !ok {
			fieldName = fmt.Sprintf("field_%d", fieldID)
		}

		fieldValue := &pb.FieldValue{
			FieldType: fieldInfo.FieldType,
		}

		// 根据字段类型设置值
		switch fieldInfo.FieldType {
		case pb.EnumFieldType_STR_FIELD:
			if fieldInfo.SimpleValue != nil {
				fieldValue.SimpleValue = fieldInfo.SimpleValue
			}
		case pb.EnumFieldType_INT_FIELD:
			if fieldInfo.SimpleValue != nil {
				fieldValue.SimpleValue = fieldInfo.SimpleValue
			}
		case pb.EnumFieldType_FLOAT_FIELD:
			if fieldInfo.SimpleValue != nil {
				fieldValue.SimpleValue = fieldInfo.SimpleValue
			}
		case pb.EnumFieldType_TIME_FIELD:
			if fieldInfo.SimpleValue != nil {
				fieldValue.SimpleValue = fieldInfo.SimpleValue
			}
			// 可以根据需要添加更多类型的处理
		}
		dataRow.Fields[fieldName] = fieldValue
	}
	return dataRow
}

// 将Options转换为SearchOptions(带字段名映射)
func convertOptions2SearchOptions(options *pb.Options, fieldName2ID map[string]uint32) *pb.SearchOptions {
	// 当options为nil时，返回一个空的SearchOptions对象，表示返回全部数据
	if options == nil {
		return &pb.SearchOptions{
			Logical: pb.Logical_LogicalAnd, // 默认逻辑关系
			MaxNum:  0,                     // 0表示不限制数量
		}
	}

	// 如果没有提供字段名映射，则创建一个
	if fieldName2ID == nil {
		// 获取全局字段名到字段ID的映射
		fieldName2ID = cache.BuildFieldName2IDMapping(0)
	}

	searchOptions := &pb.SearchOptions{
		Logical: options.Logical,
		MaxNum:  options.MaxNum,
	}

	// 转换各个部分
	searchOptions.CondGroups = convertCondGroups(options.CondGroups, fieldName2ID)
	searchOptions.Sort = convertSortOptions(options.Sort, fieldName2ID)
	searchOptions.ReturnFieldIds = convertIncludeFields(options.Includes, fieldName2ID)
	// 记录选项的详细信息
	log.DebugContextf(context.Background(), "选项转换完成: 组数=%d, 组间逻辑=%v, includes数=%d",
		len(searchOptions.CondGroups), searchOptions.Logical, len(searchOptions.ReturnFieldIds))
	return searchOptions
}

// convertCondGroups 转换条件组
func convertCondGroups(condGroups []*pb.CondGroup, fieldName2ID map[string]uint32) []*pb.SearchCondGroup {
	if len(condGroups) == 0 {
		return nil
	}

	searchCondGroups := make([]*pb.SearchCondGroup, 0, len(condGroups))
	for idx, condGroup := range condGroups {
		searchCondGroup := &pb.SearchCondGroup{
			Logical: condGroup.Logical,
			Conds:   convertConditions(condGroup.Conds, fieldName2ID),
		}
		log.DebugContextf(context.Background(), "条件组[%d]转换: 条件数=%d, 组内逻辑=%v", idx, len(searchCondGroup.Conds), searchCondGroup.Logical)
		searchCondGroups = append(searchCondGroups, searchCondGroup)
	}
	return searchCondGroups
}

// convertConditions 转换条件列表
func convertConditions(conds []*pb.Cond, fieldName2ID map[string]uint32) []*pb.SearchCond {
	if len(conds) == 0 {
		return nil
	}

	searchConds := make([]*pb.SearchCond, 0, len(conds))
	for _, cond := range conds {
		fieldID := getFieldIDFromName(cond.FieldKey, fieldName2ID)
		searchCond := &pb.SearchCond{
			FieldId: fieldID,
			Op:      cond.Op,
			Value:   cond.Value,
			MapKey:  cond.MapKey,
		}
		// 记录字段映射
		log.DebugContextf(context.Background(), "条件转换: field_key=%s -> field_id=%d, op=%v, value=%v, map_key=%s",
			cond.FieldKey, fieldID, cond.Op, cond.Value, cond.MapKey)
		searchConds = append(searchConds, searchCond)
	}
	return searchConds
}

// convertSortOptions 转换排序信息
func convertSortOptions(sorts []*pb.SortInfo, fieldName2ID map[string]uint32) []*pb.SearchSort {
	if len(sorts) == 0 {
		return nil
	}

	searchSorts := make([]*pb.SearchSort, 0, len(sorts))
	for _, sort := range sorts {
		fieldID := getFieldIDFromName(sort.FieldKey, fieldName2ID)
		searchSort := &pb.SearchSort{
			FieldId: fieldID,
			Sort:    sort.Sort,
			MapKey:  sort.MapKey,
		}
		searchSorts = append(searchSorts, searchSort)
	}
	return searchSorts
}

// convertIncludeFields 转换返回字段列表
func convertIncludeFields(includes []string, fieldName2ID map[string]uint32) []uint32 {
	if len(includes) == 0 {
		return nil
	}

	fieldIDs := make([]uint32, 0, len(includes))
	for _, include := range includes {
		fieldID := getFieldIDFromName(include, fieldName2ID)
		fieldIDs = append(fieldIDs, fieldID)
	}
	return fieldIDs
}

// getFieldIDFromName 从字段名获取字段ID
func getFieldIDFromName(fieldName string, fieldName2ID map[string]uint32) uint32 {
	fieldID, ok := fieldName2ID[fieldName]
	if !ok {
		log.Warnf("字段名 %s 未找到对应的字段ID", fieldName)
		return 0
	}
	return fieldID
}
