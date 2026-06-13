package logic

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/helper"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// SetData 创建或更新数据
func (i *accessorImpl) SetData(ctx context.Context, req *pb.SetDataReq) (*pb.SetDataRsp, error) {
	log.DebugContextf(ctx, "SetData: req=%v", req)
	// 1. 校验参数
	if err := validateSetDataParams(req); err != nil {
		log.ErrorContextf(ctx, "SetData: 参数校验失败: %v", err)
		return &pb.SetDataRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumErrorCode_INVALID_PARAM,
				Msg:  err.Error(),
			},
		}, nil
	}

	// 2. 初始化响应结构
	rsp := &pb.SetDataRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		FailedList: make(map[string]*pb.FailedDataList),
	}

	// 3. 处理每个数据列表(这里先用串行调用的方式实现)
	for _, dataList := range req.DataList {
		result := i.handleSingleDataUpdate(ctx, dataList)
		if result.err != nil {
			// 如果处理失败，立即返回错误
			log.ErrorContextf(ctx, "数据更新失败: %v", result.err)
			rsp.FailedList[result.dataKeyStr] = result.failedList
			rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
			rsp.RetInfo.Msg = fmt.Sprintf("数据更新失败: %v", result.err)
			return rsp, nil
		}

		// 发送变更通知
		if len(result.updatedRows) > 0 {
			// 只有当有更新行时才发送变更通知
			i.sendDataChangeNotification(ctx, req.AuthInfo.AppId, result)
		}
	}
	return rsp, nil
}

// sendDataChangeNotification 发送数据变更通知
func (i *accessorImpl) sendDataChangeNotification(ctx context.Context, appID string, result *DataUpdateResult) {
	// 检查是否应该为该appID发送通知
	if !i.shouldSendNotification(appID, result.dataKey.ProjectId) {
		log.DebugContextf(ctx, "跳过发送数据变更通知: appID=%s, projectID=%d 未启用或被排除", appID, result.dataKey.ProjectId)
		return
	}

	if !i.validateNotificationParams(ctx, result) {
		return
	}

	// 构建修改信息映射和失败行映射
	modifyFieldInfoMap := i.buildModifyFieldInfoMap(result)
	failedRowIDs := i.buildFailedRowIDsMap(result)

	// 发送每行的变更通知
	successCount := i.sendRowNotifications(ctx, appID, result, modifyFieldInfoMap, failedRowIDs)

	// 记录发送结果
	i.logNotificationResult(ctx, successCount)
}

// validateNotificationParams 验证通知参数
func (i *accessorImpl) validateNotificationParams(ctx context.Context, result *DataUpdateResult) bool {
	if result == nil || result.dataKey == nil || len(result.updatedRows) == 0 {
		log.WarnContextf(ctx, "发送数据变更通知: 无效的结果或数据键或无更新行")
		return false
	}
	if i.publisher == nil {
		log.WarnContextf(ctx, "发送数据变更通知: 消息发布器未初始化")
		return false
	}

	// 检查修改信息是否存在
	if result.adapterRsp == nil || len(result.adapterRsp.ModifyInfos) == 0 {
		log.WarnContextf(ctx, "发送数据变更通知: 修改信息不存在，使用有限的信息构建通知")
	}
	return true
}

// buildModifyFieldInfoMap 构建修改字段信息映射
func (i *accessorImpl) buildModifyFieldInfoMap(result *DataUpdateResult) map[string]*pb.ModifyFieldInfo {
	modifyFieldInfoMap := make(map[string]*pb.ModifyFieldInfo)
	if result.adapterRsp != nil && len(result.adapterRsp.ModifyInfos) > 0 {
		for _, modifyInfo := range result.adapterRsp.ModifyInfos {
			if modifyInfo != nil && modifyInfo.OldDocRow != nil {
				rowId := modifyInfo.OldDocRow.RowId
				modifyFieldInfoMap[rowId] = modifyInfo
			}
		}
	}
	return modifyFieldInfoMap
}

// buildFailedRowIDsMap 构建失败行ID映射
func (i *accessorImpl) buildFailedRowIDsMap(result *DataUpdateResult) map[string]struct{} {
	failedRowIDs := make(map[string]struct{})
	if result.failedList != nil && len(result.failedList.DataRows) > 0 {
		for _, failedRow := range result.failedList.DataRows {
			failedRowIDs[failedRow.RowId] = struct{}{}
		}
	}
	return failedRowIDs
}

// sendRowNotifications 发送行变更通知
func (i *accessorImpl) sendRowNotifications(ctx context.Context, appID string, result *DataUpdateResult,
	modifyFieldInfoMap map[string]*pb.ModifyFieldInfo, failedRowIDs map[string]struct{}) int {
	successCount := 0
	for _, updatedRow := range result.updatedRows {
		// 检查是否为失败的行
		if _, failed := failedRowIDs[updatedRow.RowId]; failed {
			continue
		}

		// 构建新旧行数据
		oldRow, newRow := i.buildRowData(ctx, updatedRow, result, modifyFieldInfoMap)

		// 发送单行变更通知
		if err := i.publishSingleRowChange(ctx, appID, result, updatedRow, oldRow, newRow); err != nil {
			log.ErrorContextf(ctx, "发送数据变更通知失败: 行ID=%s, 错误=%v", updatedRow.RowId, err)
		} else {
			log.DebugContextf(ctx, "发送数据变更通知成功: 对象ID=%s, 行ID=%s",
				result.dataKey.ObjectId, updatedRow.RowId)
			successCount++
		}
	}
	return successCount
}

// buildRowData 构建新旧行数据
func (i *accessorImpl) buildRowData(ctx context.Context, updatedRow *pb.UpdateDataRow, result *DataUpdateResult,
	modifyFieldInfoMap map[string]*pb.ModifyFieldInfo) (*pb.DataRow, *pb.DataRow) {
	// 创建新旧行数据结构
	newRow := &pb.DataRow{
		RowId:  updatedRow.RowId,
		Times:  updatedRow.Times,
		Fields: make(map[string]*pb.FieldValue),
	}
	oldRow := &pb.DataRow{
		RowId:  updatedRow.RowId,
		Times:  updatedRow.Times,
		Fields: make(map[string]*pb.FieldValue),
	}

	// 从修改信息中获取旧值和新值
	modifyInfo, exists := modifyFieldInfoMap[updatedRow.RowId]

	if exists && result.fieldID2Name != nil {
		i.fillRowDataFromModifyInfo(modifyInfo, result.fieldID2Name, oldRow, newRow)
	} else {
		i.fillRowDataFromUpdatedRow(ctx, updatedRow, newRow)
	}
	return oldRow, newRow
}

// fillRowDataFromModifyInfo 从修改信息填充行数据
func (i *accessorImpl) fillRowDataFromModifyInfo(modifyInfo *pb.ModifyFieldInfo,
	fieldID2Name map[uint32]string, oldRow, newRow *pb.DataRow) {
	// 从ModifyFieldInfo获取旧值
	if modifyInfo.OldDocRow != nil {
		for fieldID, fieldInfo := range modifyInfo.OldDocRow.Fields {
			if fieldName, ok := fieldID2Name[fieldID]; ok {
				oldRow.Fields[fieldName] = ConvertFieldInfoToFieldValue(fieldName, fieldInfo)
			}
		}
	}

	// 从ModifyFieldInfo获取新值
	if modifyInfo.NewDocRow != nil {
		for fieldID, fieldInfo := range modifyInfo.NewDocRow.Fields {
			if fieldName, ok := fieldID2Name[fieldID]; ok {
				newRow.Fields[fieldName] = ConvertFieldInfoToFieldValue(fieldName, fieldInfo)
			}
		}
	}
}

// fillRowDataFromUpdatedRow 从更新行填充行数据
func (i *accessorImpl) fillRowDataFromUpdatedRow(ctx context.Context, updatedRow *pb.UpdateDataRow, newRow *pb.DataRow) {
	log.WarnContextf(ctx, "行ID=%s的修改信息不存在，使用请求中的值构建通知", updatedRow.RowId)

	// 处理字段值
	for fieldName, updateField := range updatedRow.Fields {
		newRow.Fields[fieldName] = ConvertToFieldValue(fieldName, updateField)
	}
}

// publishSingleRowChange 发布单行变更
func (i *accessorImpl) publishSingleRowChange(ctx context.Context, appID string, result *DataUpdateResult,
	updatedRow *pb.UpdateDataRow, oldRow, newRow *pb.DataRow) error {
	return i.PublishDataDetailChange(ctx, &pb.DataDetailModifyMsg{
		AppId:         appID,
		ProjectId:     result.dataKey.ProjectId,
		DatasetId:     result.dataKey.DatasetId,
		ObjectId:      result.dataKey.ObjectId,
		Freq:          result.dataKey.Freq,
		Times:         updatedRow.Times,
		RowId:         updatedRow.RowId,
		OldRow:        oldRow,
		NewRow:        newRow,
		PushTimestamp: time.Now().Unix(),
	})
}

// logNotificationResult 记录通知结果
func (i *accessorImpl) logNotificationResult(ctx context.Context, successCount int) {
	if successCount == 0 {
		log.InfoContextf(ctx, "没有成功更新的数据行")
	} else {
		log.InfoContextf(ctx, "成功发送 %d 条数据行变更通知", successCount)
	}
}

// DataUpdateResult 存储处理单个UpdateDataList的结果
type DataUpdateResult struct {
	dataKeyStr   string               // 数据键字符串
	dataKey      *pb.DataKey          // 数据键
	failedList   *pb.FailedDataList   // 失败信息
	err          error                // 错误信息
	updatedRows  []*pb.UpdateDataRow  // 成功更新的行
	adapterRsp   *pb.SetFieldInfosRsp // 适配层响应
	fieldID2Name map[uint32]string    // 字段ID到字段名的映射
}

// handleSingleDataUpdate 处理单个数据列表更新
func (i *accessorImpl) handleSingleDataUpdate(ctx context.Context, dataList *pb.UpdateDataList) *DataUpdateResult {
	result := &DataUpdateResult{
		dataKey: dataList.DataKey,
	}

	// 生成数据键字符串
	result.dataKeyStr = utils.GenDataKeyStr(dataList.DataKey.ProjectId, dataList.DataKey.DatasetId,
		dataList.DataKey.ObjectId, dataList.DataKey.Freq)

	// 准备并调用适配层
	adapterReq, adapterRsp, err := i.prepareAndCallAdapter(ctx, dataList, result)
	if err != nil {
		return result
	}

	// 处理适配层响应
	i.processAdapterResponse(ctx, dataList, adapterReq, adapterRsp, result)
	return result
}

// prepareAndCallAdapter 准备并调用适配层
func (i *accessorImpl) prepareAndCallAdapter(ctx context.Context, dataList *pb.UpdateDataList,
	result *DataUpdateResult) (*SetDataAdapterReq, *pb.SetFieldInfosRsp, error) {
	// 准备适配层请求参数
	adapterReq, err := i.prepareSetDataAdapterReq(ctx, dataList)
	if err != nil {
		log.ErrorContextf(ctx, "SetData: 准备适配层请求参数失败: %v", err)
		result.failedList = genFailedDataList(dataList.DataKey, err.Error())
		result.err = err
		return nil, nil, err
	}

	// 保存字段ID到字段名的映射
	result.fieldID2Name = adapterReq.fieldID2Name

	// 调用适配层服务
	adapterClient := CreateDynamicAdapterClient(int(adapterReq.EntityId))
	adapterRsp, err := adapterClient.SetFieldInfos(ctx, adapterReq.SetFieldInfosReq)
	if err != nil {
		log.ErrorContextf(ctx, "SetData: 调用适配层服务失败: %v", err)
		result.failedList = genFailedDataList(dataList.DataKey, fmt.Sprintf("调用适配层服务失败: %v", err))
		result.err = err
		return nil, nil, err
	}

	// 保存适配层响应
	result.adapterRsp = adapterRsp

	// 检查适配层响应状态
	if adapterRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
		log.ErrorContextf(ctx, "SetData: 适配层返回错误: code=%v, msg=%v",
			adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg)
		result.failedList = genFailedDataList(dataList.DataKey, adapterRsp.RetInfo.Msg)
		result.err = fmt.Errorf("适配层返回错误: %s", adapterRsp.RetInfo.Msg)
		return nil, nil, fmt.Errorf("适配层返回错误")
	}

	return adapterReq, adapterRsp, nil
}

// processAdapterResponse 处理适配层响应
func (i *accessorImpl) processAdapterResponse(ctx context.Context, dataList *pb.UpdateDataList,
	adapterReq *SetDataAdapterReq, adapterRsp *pb.SetFieldInfosRsp, result *DataUpdateResult) {
	// 初始化结果
	result.updatedRows = make([]*pb.UpdateDataRow, 0, len(dataList.DataRows))
	failedRowIDs := make(map[string]struct{})

	// 处理失败的行
	if len(adapterRsp.FailedRows) > 0 {
		if err := i.processFailedRows(ctx, dataList, adapterReq, adapterRsp, result, failedRowIDs); err != nil {
			return
		}
	}

	// 记录成功更新的行
	for _, updateRow := range dataList.DataRows {
		if _, failed := failedRowIDs[updateRow.RowId]; !failed {
			result.updatedRows = append(result.updatedRows, updateRow)
		}
	}
}

// processFailedRows 处理失败的行
func (i *accessorImpl) processFailedRows(ctx context.Context, dataList *pb.UpdateDataList,
	adapterReq *SetDataAdapterReq, adapterRsp *pb.SetFieldInfosRsp, result *DataUpdateResult,
	failedRowIDs map[string]struct{}) error {
	failedDataRows, err := convertFailedDataRows(adapterRsp.FailedRows, adapterReq.fieldID2Name)
	if err != nil {
		log.ErrorContextf(ctx, "SetData: 转换失败行结果失败: %v", err)
		result.failedList = genFailedDataList(dataList.DataKey, "转换失败行结果失败")
		result.err = err
		return err
	}

	result.failedList = &pb.FailedDataList{
		DataKey:  dataList.DataKey,
		DataRows: failedDataRows,
	}
	result.err = fmt.Errorf("部分数据更新失败")

	// 记录失败的行ID
	for _, failedRow := range failedDataRows {
		failedRowIDs[failedRow.RowId] = struct{}{}
	}
	return nil
}

// validateSetDataParams 验证请求参数
func validateSetDataParams(req *pb.SetDataReq) error {
	if req.AuthInfo == nil {
		return fmt.Errorf("auth_info is nil")
	}
	if len(req.DataList) == 0 {
		return fmt.Errorf("data_list is empty")
	}

	for i, dataList := range req.DataList {
		if dataList.DataKey == nil {
			return fmt.Errorf("data_list[%d].data_key is nil", i)
		}
		if dataList.DataKey.ProjectId <= 0 {
			return fmt.Errorf("invalid project_id: %d in data_list[%d]", dataList.DataKey.ProjectId, i)
		}
		if dataList.DataKey.DatasetId <= 0 {
			return fmt.Errorf("invalid dataset_id: %d in data_list[%d]", dataList.DataKey.DatasetId, i)
		}
		if dataList.DataKey.ObjectId == "" {
			return fmt.Errorf("empty object_id in data_list[%d]", i)
		}
		// 校验ObjectID格式
		if err := ValidateObjectID(dataList.DataKey.ObjectId); err != nil {
			return fmt.Errorf("invalid object_id in data_list[%d]: %v", i, err)
		}
		// 校验频率格式，允许为空
		if dataList.DataKey.Freq != "" {
			if err := ValidateFreq(dataList.DataKey.Freq); err != nil {
				return fmt.Errorf("invalid freq in data_list[%d]: %v", i, err)
			}
		}
		if len(dataList.DataRows) == 0 {
			return fmt.Errorf("data_list[%d].data_rows is empty", i)
		}

		// 检查DataRows行数限制
		dataRowsCount := uint32(len(dataList.DataRows))
		// 获取配置中的最大行数限制
		cfg := config.GetGlobalConfig()
		maxUpdateRows := uint32(25) // 默认值
		if cfg != nil && cfg.Limits.MaxUpdateRows > 0 {
			maxUpdateRows = cfg.Limits.MaxUpdateRows
		}

		if dataRowsCount > maxUpdateRows {
			return fmt.Errorf("moox backend service: data_list[%d].data_rows行数超过限制，当前%d行，最大允许%d行", i, dataRowsCount, maxUpdateRows)
		}
	}
	return nil
}

// SetDataAdapterReq 用于构建适配层请求的扩展结构
type SetDataAdapterReq struct {
	*pb.SetFieldInfosReq
	fieldID2Name map[uint32]string // 用于保存字段ID到字段名的映射
	EntityId     uint32
}

// prepareSetDataAdapterReq 准备适配层请求
func (i *accessorImpl) prepareSetDataAdapterReq(ctx context.Context, dataList *pb.UpdateDataList) (*SetDataAdapterReq, error) {
	// 1. 生成表ID
	tableID := utils.GenDataTableID(dataList.DataKey.DatasetId, dataList.DataKey.ObjectId, dataList.DataKey.Freq)
	log.DebugContextf(ctx, "使用表ID: %s", tableID)

	// 2. 获取该项目-dataset下挂的数据详情字段列表
	detailFields, err := cache.GetDetailFieldList(ctx, dataList.DataKey.ProjectId, dataList.DataKey.DatasetId)
	if err != nil {
		return nil, fmt.Errorf("获取数据详情字段列表失败: %v", err)
	}
	if len(detailFields) == 0 {
		return nil, fmt.Errorf("未找到数据集的详情字段")
	}

	// 3. 创建字段名到字段ID的映射
	nameToID, idToName, fieldMap := BuildFieldMappings(detailFields, true)

	// 4. 获取数据集的存储路由
	entityID, err := GetDataRoute(dataList.DataKey.DatasetId, dataList.DataKey.ObjectId)
	if err != nil {
		return nil, err
	}

	// 5. 转换数据行到文档行
	updateDocRows, err := convertToDocRows(ctx, dataList.DataRows, nameToID, fieldMap)
	if err != nil {
		return nil, fmt.Errorf("转换数据行失败: %v", err)
	}

	// 6. 构建适配层请求
	return &SetDataAdapterReq{
		SetFieldInfosReq: &pb.SetFieldInfosReq{
			TableId:       tableID,
			UpdateDocRows: updateDocRows,
			DataType:      getDataTypeFromDataset(int(dataList.DataKey.DatasetId)),
		},
		fieldID2Name: idToName,
		EntityId:     uint32(entityID),
	}, nil
}

// 将UpdateDataRow转换为UpdateDocRow
func convertToDocRows(ctx context.Context, dataRows []*pb.UpdateDataRow, nameToID map[string]uint32, fieldMap map[string]*cache.Field) ([]*pb.UpdateDocRow, error) {
	var updateDocRows []*pb.UpdateDocRow

	for _, dataRow := range dataRows {
		// 检查RowId是否为空，如果为空则生成新的行ID
		if dataRow.RowId == "" {
			dataRow.RowId = helper.GenRowID()
			log.DebugContextf(ctx, "为数据行生成新的行ID: %s", dataRow.RowId)
		}

		updateDocRow := &pb.UpdateDocRow{
			RowId:  dataRow.RowId,
			Times:  dataRow.Times,
			Fields: make(map[uint32]*pb.UpdateFieldInfo),
		}

		// 将字段名转换为字段ID，并转换字段值
		for fieldName, updateField := range dataRow.Fields {
			fieldID, ok := nameToID[fieldName]
			if !ok {
				return nil, fmt.Errorf("行ID=%s: 字段名'%s'不存在", dataRow.RowId, fieldName)
			}

			// 获取字段定义进行验证
			field, exists := fieldMap[fieldName]
			if !exists {
				log.WarnContextf(ctx, "找不到字段结构: %s，跳过验证", fieldName)
			} else {
				// 验证字段值 - 任何验证失败都直接返回错误
				if err := ValidateFieldValue(field, updateField); err != nil {
					log.ErrorContextf(ctx, "字段值验证失败: 行ID=%s, 字段=%s, 错误=%v",
						dataRow.RowId, fieldName, err)
					return nil, fmt.Errorf("行ID=%s, 字段'%s': %v", dataRow.RowId, fieldName, err)
				}
			}

			updateFieldInfo := convertFieldInfo(fieldID, updateField)
			updateDocRow.Fields[fieldID] = updateFieldInfo
		}
		updateDocRows = append(updateDocRows, updateDocRow)
	}

	if len(updateDocRows) == 0 {
		return nil, fmt.Errorf("没有有效的数据行")
	}
	return updateDocRows, nil
}

// 将FailedDocRow转换为FailedDataRow
func convertFailedDataRows(failedDocRows []*pb.FailedDocRow, fieldID2Name map[uint32]string) ([]*pb.FailedDataRow, error) {
	var failedDataRows []*pb.FailedDataRow

	for _, failedDocRow := range failedDocRows {
		failedDataRow := &pb.FailedDataRow{
			RowId:      failedDocRow.RowId,
			FailedList: make(map[string]*pb.FailedInfo),
		}

		// 转换失败字段列表（从ID转为字段名）
		for fieldID, failedInfo := range failedDocRow.FailedList {
			fieldName, ok := fieldID2Name[fieldID]
			if !ok {
				fieldName = fmt.Sprintf("field_%d", fieldID) // 使用默认名称
			}
			failedDataRow.FailedList[fieldName] = failedInfo
		}

		failedDataRows = append(failedDataRows, failedDataRow)
	}
	return failedDataRows, nil
}

// 将UpdateField转换为UpdateFieldInfo
func convertFieldInfo(fieldID uint32, updateField *pb.UpdateField) *pb.UpdateFieldInfo {
	updateFieldInfo := &pb.UpdateFieldInfo{
		UpdateType: updateField.GetUpdateType(),
		FieldInfo: &pb.FieldInfo{
			FieldId:   fieldID,
			FieldType: updateField.GetFieldType(),
		},
	}

	// 根据UpdateField的字段值设置FieldInfo的值
	if updateField.GetSimpleValue() != nil {
		updateFieldInfo.FieldInfo.SimpleValue = updateField.GetSimpleValue()
	} else if updateField.GetMapValue() != nil {
		updateFieldInfo.FieldInfo.MapValue = updateField.GetMapValue()
	}
	return updateFieldInfo
}
