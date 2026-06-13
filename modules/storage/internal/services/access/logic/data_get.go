package logic

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// GetData 获取数据详情接口
func (i *accessorImpl) GetData(ctx context.Context, req *pb.GetDataReq) (*pb.GetDataRsp, error) {
	log.DebugContextf(ctx, "GetData: req=%v", req)
	// TODO:权限校验&频控
	// 1. 校验参数
	if err := validateGetDataParams(req); err != nil {
		log.ErrorContextf(ctx, "GetData: 参数校验失败: %v", err)
		return &pb.GetDataRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumErrorCode_INVALID_PARAM,
				Msg:  err.Error(),
			},
		}, nil
	}

	// 2. 初始化响应结构
	rsp := &pb.GetDataRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		DataList:   make(map[string]*pb.DataList),
		FailedList: make(map[string]*pb.FailedDataList),
	}

	// 3. 并发处理每个数据参数（每个数据表）
	resultChan := make(chan *DataFetchResult, len(req.DataParams))
	defer close(resultChan)

	var handlers []func() error
	for _, param := range req.DataParams {
		paramCopy := param
		handlers = append(handlers, func() error {
			result := i.handleSingleDataFetch(ctx, paramCopy)
			resultChan <- result
			return nil
		})
	}
	err := trpc.GoAndWait(handlers...)

	// 收集结果
	for range req.DataParams {
		result := <-resultChan
		dataKeyStr := result.dataKeyStr
		if result.err != nil || result.dataRows == nil {
			rsp.FailedList[dataKeyStr] = result.failedList
		} else {
			rsp.DataList[dataKeyStr] = &pb.DataList{
				DataKey:  result.dataKey,
				DataRows: result.dataRows,
			}
		}
	}
	return rsp, err
}

// DataFetchResult 存储处理单个DataParam的结果
type DataFetchResult struct {
	dataKeyStr string             // 数据键字符串
	dataKey    *pb.DataKey        // 数据键
	dataRows   []*pb.DataRow      // 成功的数据行
	failedList *pb.FailedDataList // 失败信息
	err        error              // 错误信息
}

// handleSingleDataFetch 处理单个数据参数
func (i *accessorImpl) handleSingleDataFetch(ctx context.Context, param *pb.GetDataParams) *DataFetchResult {
	result := &DataFetchResult{
		dataKey: param.DataKey,
	}

	// 生成数据键字符串
	result.dataKeyStr = utils.GenDataKeyStr(param.DataKey.ProjectId, param.DataKey.DatasetId,
		param.DataKey.ObjectId, param.DataKey.Freq)

	// 1. 准备适配层请求参数
	adapterReq, err := i.prepareGetDataAdapterReq(ctx, param)
	if err != nil {
		log.ErrorContextf(ctx, "GetData: 准备适配层请求参数失败: %v", err)
		result.failedList = genFailedDataList(param.DataKey, err.Error())
		result.err = err
		return result
	}

	// 2. 调用适配层服务
	adapterRsp, err := i.callAdapterGetFieldInfos(ctx, adapterReq.EntityId, adapterReq.GetFieldInfosReq)
	if err != nil {
		log.ErrorContextf(ctx, "GetData: 调用适配层服务失败: %v", err)
		result.failedList = genFailedDataList(param.DataKey, fmt.Sprintf("调用适配层服务失败: %v", err))
		result.err = err
		return result
	}

	// 3. 检查适配层响应状态
	if adapterRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
		log.ErrorContextf(ctx, "GetData: 适配层返回错误: code=%v, msg=%v",
			adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg)
		result.failedList = genFailedDataList(param.DataKey, adapterRsp.RetInfo.Msg)
		result.err = fmt.Errorf("适配层返回错误: %s", adapterRsp.RetInfo.Msg)
		return result
	}

	// 4. 转换响应结果
	dataRows, err := convertToDataRows(ctx, adapterRsp.DocRows, adapterReq.fieldID2Name)
	if err != nil {
		log.ErrorContextf(ctx, "GetData: 转换结果失败: %v", err)
		result.failedList = genFailedDataList(param.DataKey, "转换结果失败")
		result.err = err
		return result
	}

	// 5. 返回成功结果
	result.dataRows = dataRows
	return result
}

// validateGetDataParams 验证请求参数
func validateGetDataParams(req *pb.GetDataReq) error {
	if req.AuthInfo == nil {
		return fmt.Errorf("auth_info is nil")
	}
	if len(req.DataParams) == 0 {
		return fmt.Errorf("data_params is empty")
	}

	for i, param := range req.DataParams {
		if param.DataKey == nil {
			return fmt.Errorf("data_params[%d].data_key is nil", i)
		}
		if param.DataKey.ProjectId <= 0 {
			return fmt.Errorf("invalid project_id: %d in data_params[%d]", param.DataKey.ProjectId, i)
		}
		if param.DataKey.DatasetId <= 0 {
			return fmt.Errorf("invalid dataset_id: %d in data_params[%d]", param.DataKey.DatasetId, i)
		}
		if param.DataKey.ObjectId == "" {
			return fmt.Errorf("empty object_id in data_params[%d]", i)
		}
		// 校验ObjectID格式
		if err := ValidateObjectID(param.DataKey.ObjectId); err != nil {
			return fmt.Errorf("invalid object_id in data_params[%d]: %v", i, err)
		}
		// 校验频率格式，允许为空
		if param.DataKey.Freq != "" {
			if err := ValidateFreq(param.DataKey.Freq); err != nil {
				return fmt.Errorf("invalid freq in data_params[%d]: %v", i, err)
			}
		}
	}
	return nil
}

// GetDataAdapterReq 用于构建适配层请求的扩展结构
type GetDataAdapterReq struct {
	*pb.GetFieldInfosReq
	EntityId     uint32
	fieldID2Name map[uint32]string // 用于保存字段ID到字段名的映射
}

// prepareGetDataAdapterReq 准备适配层请求
func (i *accessorImpl) prepareGetDataAdapterReq(ctx context.Context, param *pb.GetDataParams) (*GetDataAdapterReq, error) {
	// 1. 生成表ID
	tableID := utils.GenDataTableID(param.DataKey.DatasetId, param.DataKey.ObjectId, param.DataKey.Freq)
	log.DebugContextf(ctx, "使用表ID: %s", tableID)

	// 2. 获取该项目-dataset下挂的数据详情字段列表
	detailFields, err := cache.GetDetailFieldList(ctx, param.DataKey.ProjectId, param.DataKey.DatasetId)
	if err != nil {
		return nil, fmt.Errorf("获取数据详情字段列表失败: %v", err)
	}
	if len(detailFields) == 0 {
		return nil, fmt.Errorf("未找到数据集的详情字段")
	}

	// 3. 将字段名转换为字段ID
	fieldIDs, fieldID2Name, err := convertNamesToIDs(param.FieldKeys, detailFields)
	if err != nil {
		return nil, fmt.Errorf("转换字段名称失败: %v", err)
	}

	// 4. 转换MapKeys
	mapKeys, err := convertMapKeys(param.MapKeys, fieldID2Name)
	if err != nil {
		return nil, fmt.Errorf("转换Map字段键失败: %v", err)
	}

	// 5. 获取数据集的默认存储路由
	entityId, err := GetDataRoute(param.DataKey.DatasetId, param.DataKey.ObjectId)
	if err != nil {
		return nil, err
	}

	// 6. 构建时间区间
	timeInterval := BuildTimeInterval(ctx, param.TimeRange, param.DataKey.Freq)

	// 7. 构建适配层请求
	adapterReq := &GetDataAdapterReq{
		GetFieldInfosReq: &pb.GetFieldInfosReq{
			TableId:      tableID,
			DataType:     getDataTypeFromDataset(int(param.DataKey.DatasetId)),
			FieldIds:     fieldIDs,
			MapKeys:      mapKeys,
			TimeInterval: timeInterval,
			RowId:        param.RowId,
			MaxLimit:     param.MaxLimit,
		},
		fieldID2Name: fieldID2Name,
		EntityId:     uint32(entityId),
	}
	return adapterReq, nil
}

// 将DocRows转换为DataRows
func convertToDataRows(ctx context.Context, docRows []*pb.DocRow, fieldID2Name map[uint32]string) ([]*pb.DataRow, error) {
	var dataRows []*pb.DataRow
	for _, docRow := range docRows {
		dataRow := &pb.DataRow{
			Times:  docRow.Times, // 使用DocRow中的Times字段
			RowId:  docRow.RowId,
			Fields: make(map[string]*pb.FieldValue),
		}

		// 转换字段
		for fieldID, fieldInfo := range docRow.Fields {
			fieldName, ok := fieldID2Name[fieldID]
			if !ok {
				// 如果找不到对应的字段名，记录日志并跳过
				log.WarnContextf(ctx, "找不到字段ID %d 对应的字段名，跳过该字段", fieldID)
				continue
			}

			// 转换字段值
			fieldValue, err := convertFieldValue(fieldName, fieldInfo)
			if err != nil {
				log.WarnContextf(ctx, "转换字段值失败: %v", err)
				continue
			}
			dataRow.Fields[fieldName] = fieldValue
		}
		dataRows = append(dataRows, dataRow)
	}
	return dataRows, nil
}

// convertFieldValue 转换字段信息为字段值
func convertFieldValue(fieldName string, fieldInfo *pb.FieldInfo) (*pb.FieldValue, error) {
	fieldValue := &pb.FieldValue{
		FieldKey:  fieldName,
		FieldType: fieldInfo.FieldType,
	}

	// 处理映射类型
	if fieldInfo.FieldType == pb.EnumFieldType_MAP_KV_FIELD {
		if fieldInfo.MapValue == nil {
			return fieldValue, nil
		}
		fieldValue.MapValue = convertMapValue(fieldInfo.MapValue)
		return fieldValue, nil
	}

	// 处理简单类型
	if fieldInfo.SimpleValue == nil || fieldInfo.SimpleValue.Value == nil {
		return fieldValue, nil
	}

	// 根据字段类型转换值
	var err error
	fieldValue.SimpleValue, err = convertSimpleValue(fieldInfo.FieldType, fieldInfo.SimpleValue)
	if err != nil {
		return nil, err
	}
	return fieldValue, nil
}

// convertSimpleValue 转换简单类型的值
func convertSimpleValue(fieldType pb.EnumFieldType, simpleValue *pb.SimpleValue) (*pb.SimpleValue, error) {
	switch fieldType {
	case pb.EnumFieldType_STR_FIELD:
		if strVal, ok := simpleValue.Value.(*pb.SimpleValue_Str); ok {
			return &pb.SimpleValue{
				Value: &pb.SimpleValue_Str{Str: strVal.Str},
			}, nil
		}
	case pb.EnumFieldType_INT_FIELD:
		if intVal, ok := simpleValue.Value.(*pb.SimpleValue_Int); ok {
			return &pb.SimpleValue{
				Value: &pb.SimpleValue_Int{Int: intVal.Int},
			}, nil
		}
	case pb.EnumFieldType_FLOAT_FIELD:
		if floatVal, ok := simpleValue.Value.(*pb.SimpleValue_Float); ok {
			return &pb.SimpleValue{
				Value: &pb.SimpleValue_Float{Float: floatVal.Float},
			}, nil
		}
	case pb.EnumFieldType_TIME_FIELD:
		if timeVal, ok := simpleValue.Value.(*pb.SimpleValue_Time); ok {
			return &pb.SimpleValue{
				Value: &pb.SimpleValue_Time{Time: timeVal.Time},
			}, nil
		}
	case pb.EnumFieldType_INT_VEC_FIELD:
		if intList, ok := simpleValue.Value.(*pb.SimpleValue_IntList); ok && intList.IntList != nil {
			return &pb.SimpleValue{
				Value: &pb.SimpleValue_IntList{IntList: intList.IntList},
			}, nil
		}
	case pb.EnumFieldType_SET_FIELD:
		if strList, ok := simpleValue.Value.(*pb.SimpleValue_StrList); ok && strList.StrList != nil {
			return &pb.SimpleValue{
				Value: &pb.SimpleValue_StrList{StrList: strList.StrList},
			}, nil
		}
	default:
		return nil, fmt.Errorf("未支持的字段类型: %v", fieldType)
	}
	return nil, nil
}

// convertMapValue 转换映射类型的值
func convertMapValue(mapValue *pb.MapContainer) *pb.MapContainer {
	if mapValue == nil {
		return nil
	}

	result := &pb.MapContainer{
		Entries: make(map[string]*pb.KeyValueEntry),
	}

	// 转换每个map值
	for key, entry := range mapValue.Entries {
		// 只支持字符串值，忽略其他类型
		if entry.Type != pb.EnumFieldType_STR_FIELD || entry.Value == nil {
			continue
		}

		strVal, ok := entry.Value.Value.(*pb.SimpleValue_Str)
		if !ok {
			continue
		}

		result.Entries[key] = &pb.KeyValueEntry{
			Type: pb.EnumFieldType_STR_FIELD,
			Value: &pb.SimpleValue{
				Value: &pb.SimpleValue_Str{Str: strVal.Str},
			},
		}
	}
	return result
}

// genFailedDataList 生成失败数据列表
func genFailedDataList(dataKey *pb.DataKey, errMsg string) *pb.FailedDataList {
	return &pb.FailedDataList{
		DataKey: dataKey,
		DataRows: []*pb.FailedDataRow{
			{
				FailedList: map[string]*pb.FailedInfo{
					"_system": {
						Code: pb.EnumErrorCode_INNER_ERR,
						Msg:  errMsg,
					},
				},
			},
		},
	}
}
