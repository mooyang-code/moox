package logic

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// UpsertObject 实现创建或更新数据对象接口（注意：数据集中的全局对象列表存储在“路由配置”，数据集默认配置的存储设备中）
func (i *accessorImpl) UpsertObject(ctx context.Context, req *pb.UpsertObjectReq) (*pb.UpsertObjectRsp, error) {
	log.DebugContextf(ctx, "UpsertObject: req=%v", req)
	// 1. 校验参数
	if err := validateUpsertObjectParams(req); err != nil {
		log.ErrorContextf(ctx, "UpsertObject: 参数校验失败: %v", err)
		return genUpsertObjectRsp(pb.EnumErrorCode_INVALID_PARAM, err.Error()), nil
	}

	// 2. 准备适配层请求参数
	adapterReq, err := i.prepareUpsertReq(ctx, req)
	if err != nil {
		code := getErrorCode(err)
		log.ErrorContextf(ctx, "UpsertObject: 准备适配层请求参数失败: %v", err)
		return genUpsertObjectRsp(code, err.Error()), nil
	}

	// 3. 调用适配层服务
	// 创建动态适配层客户端
	adapterClient := CreateDynamicAdapterClient(int(adapterReq.EntityId))

	// 调用适配层服务
	adapterRsp, err := adapterClient.SetFieldInfos(ctx, adapterReq.SetFieldInfosReq)
	if err != nil {
		log.ErrorContextf(ctx, "UpsertObject: 调用适配层服务失败: %v", err)
		return genUpsertObjectRsp(pb.EnumErrorCode_INNER_ERR, fmt.Sprintf("调用适配层服务失败: %v", err)), nil
	}

	// 4. 检查适配层响应状态
	if adapterRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
		log.ErrorContextf(ctx, "UpsertObject: 适配层返回错误: code=%v, msg=%v",
			adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg)
		return genUpsertObjectRsp(adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg), nil
	}

	// 5. 处理失败的行
	var failedRows []*pb.FailedObjectRow
	if len(adapterRsp.FailedRows) > 0 {
		failedRows, err = convertFailedObjectRows(adapterRsp.FailedRows, adapterReq.fieldID2Name)
		if err != nil {
			log.ErrorContextf(ctx, "UpsertObject: 转换失败行结果失败: %v", err)
			return genUpsertObjectRsp(pb.EnumErrorCode_INNER_ERR, "转换失败行结果失败"), nil
		}
	}

	// 5.1 发送对象变更通知
	i.sendObjectChangeNotifications(ctx, req, adapterRsp, adapterReq.fieldID2Name)

	// 6. 组装返回结果
	return &pb.UpsertObjectRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		FailedRows: failedRows,
	}, nil
}

// sendObjectChangeNotifications 发送对象变更通知
func (i *accessorImpl) sendObjectChangeNotifications(ctx context.Context, req *pb.UpsertObjectReq,
	adapterRsp *pb.SetFieldInfosRsp, fieldID2Name map[uint32]string) {
	// 检查是否应该为该appID发送通知
	if !i.shouldSendNotification(req.AuthInfo.AppId, req.ProjectId) {
		log.DebugContextf(ctx, "跳过发送对象变更通知: appID=%s, projectID=%d 未启用或被排除", req.AuthInfo.AppId, req.ProjectId)
		return
	}

	if i.publisher == nil {
		log.WarnContextf(ctx, "发送对象变更通知: 消息发布器未初始化")
		return
	}

	// 构建映射关系
	failedObjectIDs := i.buildFailedObjectIDsMap(adapterRsp)
	modifyFieldInfoMap := i.buildObjectModifyFieldInfoMap(adapterRsp)

	// 发送变更通知
	successCount := i.sendObjectNotifications(ctx, req, failedObjectIDs, modifyFieldInfoMap, fieldID2Name)

	// 记录结果
	i.logObjectNotificationResult(ctx, successCount)
}

// buildFailedObjectIDsMap 构建失败对象ID映射
func (i *accessorImpl) buildFailedObjectIDsMap(adapterRsp *pb.SetFieldInfosRsp) map[string]struct{} {
	failedObjectIDs := make(map[string]struct{})
	if adapterRsp != nil && len(adapterRsp.FailedRows) > 0 {
		for _, failedRow := range adapterRsp.FailedRows {
			failedObjectIDs[failedRow.RowId] = struct{}{}
		}
	}
	return failedObjectIDs
}

// buildObjectModifyFieldInfoMap 构建对象修改字段信息映射
func (i *accessorImpl) buildObjectModifyFieldInfoMap(adapterRsp *pb.SetFieldInfosRsp) map[string]*pb.ModifyFieldInfo {
	modifyFieldInfoMap := make(map[string]*pb.ModifyFieldInfo)
	if adapterRsp != nil && len(adapterRsp.ModifyInfos) > 0 {
		for _, modifyInfo := range adapterRsp.ModifyInfos {
			if modifyInfo != nil && modifyInfo.OldDocRow != nil {
				rowId := modifyInfo.OldDocRow.RowId
				modifyFieldInfoMap[rowId] = modifyInfo
			}
		}
	}
	return modifyFieldInfoMap
}

// sendObjectNotifications 发送对象变更通知
func (i *accessorImpl) sendObjectNotifications(ctx context.Context, req *pb.UpsertObjectReq,
	failedObjectIDs map[string]struct{}, modifyFieldInfoMap map[string]*pb.ModifyFieldInfo,
	fieldID2Name map[uint32]string) int {
	successCount := 0
	for _, objectRow := range req.ObjectRows {
		// 检查是否为失败的对象
		if _, failed := failedObjectIDs[objectRow.ObjectId]; failed {
			continue
		}

		// 获取修改信息
		modifyInfo, exists := modifyFieldInfoMap[objectRow.ObjectId]
		if !exists || modifyInfo.OldDocRow == nil {
			log.DebugContextf(ctx, "对象ID=%s的修改信息不存在，使用有限的信息构建通知", objectRow.ObjectId)
			continue
		}

		// 构建新旧行数据
		oldRow, newRow := i.buildObjectRowData(objectRow.ObjectId, modifyInfo, fieldID2Name)

		// 发送通知
		if err := i.publishObjectChange(ctx, req, objectRow.ObjectId, oldRow, newRow); err != nil {
			log.ErrorContextf(ctx, "发送对象变更通知失败: 对象ID=%s, 错误=%v", objectRow.ObjectId, err)
		} else {
			log.DebugContextf(ctx, "发送对象变更通知成功: 对象ID=%s", objectRow.ObjectId)
			successCount++
		}
	}
	return successCount
}

// buildObjectRowData 构建对象行数据
func (i *accessorImpl) buildObjectRowData(objectID string, modifyInfo *pb.ModifyFieldInfo,
	fieldID2Name map[uint32]string) (*pb.ObjectRow, *pb.ObjectRow) {
	// 创建新旧行数据结构
	newRow := &pb.ObjectRow{
		ObjectId: objectID,
		Fields:   make(map[string]*pb.FieldValue),
	}
	oldRow := &pb.ObjectRow{
		ObjectId: objectID,
		Fields:   make(map[string]*pb.FieldValue),
	}

	// 填充旧值
	if modifyInfo.OldDocRow != nil {
		for fieldID, fieldInfo := range modifyInfo.OldDocRow.Fields {
			if fieldName, ok := fieldID2Name[fieldID]; ok {
				oldRow.Fields[fieldName] = ConvertFieldInfoToFieldValue(fieldName, fieldInfo)
			}
		}
	}

	// 填充新值
	if modifyInfo.NewDocRow != nil {
		for fieldID, fieldInfo := range modifyInfo.NewDocRow.Fields {
			if fieldName, ok := fieldID2Name[fieldID]; ok {
				newRow.Fields[fieldName] = ConvertFieldInfoToFieldValue(fieldName, fieldInfo)
			}
		}
	}
	return oldRow, newRow
}

// publishObjectChange 发布对象变更
func (i *accessorImpl) publishObjectChange(ctx context.Context, req *pb.UpsertObjectReq,
	objectID string, oldRow, newRow *pb.ObjectRow) error {
	return i.PublishObjectChange(ctx, &pb.ObjectModifyMsg{
		AppId:         req.AuthInfo.AppId,
		ProjectId:     req.ProjectId,
		DatasetId:     req.DatasetId,
		ObjectId:      objectID,
		OldRow:        oldRow,
		NewRow:        newRow,
		PushTimestamp: time.Now().Unix(),
	})
}

// logObjectNotificationResult 记录对象通知结果
func (i *accessorImpl) logObjectNotificationResult(ctx context.Context, successCount int) {
	if successCount == 0 {
		log.DebugContextf(ctx, "没有成功更新的对象")
	} else {
		log.DebugContextf(ctx, "成功发送 %d 个对象变更通知", successCount)
	}
}

// validateUpsertObjectParams 验证请求参数
func validateUpsertObjectParams(req *pb.UpsertObjectReq) error {
	if req.AuthInfo == nil {
		return fmt.Errorf("auth_info is nil")
	}
	if req.ProjectId <= 0 {
		return fmt.Errorf("invalid project_id: %d", req.ProjectId)
	}
	if req.DatasetId <= 0 {
		return fmt.Errorf("invalid dataset_id: %d", req.DatasetId)
	}
	if len(req.ObjectRows) == 0 {
		return fmt.Errorf("object_rows is empty")
	}

	// 校验每个对象行的ObjectID
	for i, objectRow := range req.ObjectRows {
		if objectRow.ObjectId == "" {
			return fmt.Errorf("empty object_id in object_rows[%d]", i)
		}
		// 校验ObjectID格式
		if err := ValidateObjectID(objectRow.ObjectId); err != nil {
			return fmt.Errorf("invalid object_id in object_rows[%d]: %v", i, err)
		}
	}
	return nil
}

// SetFieldInfosAdapterReq 用于构建适配层请求的扩展结构
type SetFieldInfosAdapterReq struct {
	*pb.SetFieldInfosReq
	fieldID2Name map[uint32]string // 用于保存字段ID到字段名的映射
	ObjectID     string            // 用于标识对象ID
	EntityId     uint32
}

// prepareUpsertReq 准备适配层请求
func (i *accessorImpl) prepareUpsertReq(ctx context.Context, req *pb.UpsertObjectReq) (*SetFieldInfosAdapterReq, error) {
	// 1. 生成表ID
	tableID := utils.GenObjectTableID(req.DatasetId)
	log.DebugContextf(ctx, "使用表ID: %s", tableID)

	// 2. 获取元数据字段列表
	metaFields, err := cache.GetMetaFieldList(ctx, req.ProjectId, req.DatasetId)
	if err != nil {
		return nil, fmt.Errorf("获取元数据字段列表失败: %v", err)
	}
	if len(metaFields) == 0 {
		return nil, fmt.Errorf("未找到数据集的元数据字段")
	}

	// 3. 获取dataset的默认存储路由
	entityID, err := GetDataRoute(req.DatasetId, "*")
	if err != nil {
		return nil, fmt.Errorf("未找到默认对象路由")
	}

	// 4. 使用公共函数构建字段映射
	nameToID, idToName, fieldMap := BuildFieldMappings(metaFields, true)

	// 5. 处理对象行
	updateDocRows, err := i.processObjectRows(ctx, req.ObjectRows, nameToID, fieldMap)
	if err != nil {
		return nil, err
	}

	// 6. 构建适配层请求
	return i.buildAdapterRequest(entityID, tableID, updateDocRows, idToName), nil
}

// processObjectRows 处理对象行数据
func (i *accessorImpl) processObjectRows(ctx context.Context, objectRows []*pb.UpdateObjectRow,
	nameToID map[string]uint32, fieldMap map[string]*cache.Field) ([]*pb.UpdateDocRow, error) {
	var updateDocRows []*pb.UpdateDocRow

	for _, objectRow := range objectRows {
		updateDocRow := &pb.UpdateDocRow{
			RowId:  objectRow.ObjectId,
			Fields: make(map[uint32]*pb.UpdateFieldInfo),
		}

		// 处理每个字段
		for fieldName, updateField := range objectRow.Fields {
			fieldID, ok := nameToID[fieldName]
			if !ok {
				return nil, fmt.Errorf("对象ID=%s: 字段名'%s'不存在", objectRow.ObjectId, fieldName)
			}

			field, exists := fieldMap[fieldName]
			if !exists {
				log.WarnContextf(ctx, "找不到字段结构: %s，跳过验证", fieldName)
			} else {
				// 验证字段值 - 任何验证失败都直接返回错误
				if err := ValidateFieldValue(field, updateField); err != nil {
					log.ErrorContextf(ctx, "字段值验证失败: 对象ID=%s, 字段=%s, 错误=%v",
						objectRow.ObjectId, fieldName, err)
					return nil, fmt.Errorf("对象ID=%s, 字段'%s': %v", objectRow.ObjectId, fieldName, err)
				}
			}

			updateFieldInfo := convertFieldInfo(fieldID, updateField)
			updateDocRow.Fields[fieldID] = updateFieldInfo
		}

		updateDocRows = append(updateDocRows, updateDocRow)
	}

	if len(updateDocRows) == 0 {
		return nil, fmt.Errorf("没有有效的对象行")
	}
	return updateDocRows, nil
}

// buildAdapterRequest 构建适配层请求
func (i *accessorImpl) buildAdapterRequest(entityID int, tableID string,
	updateDocRows []*pb.UpdateDocRow, idToName map[uint32]string) *SetFieldInfosAdapterReq {
	return &SetFieldInfosAdapterReq{
		SetFieldInfosReq: &pb.SetFieldInfosReq{
			TableId:       tableID,
			UpdateDocRows: updateDocRows,
			DataType:      pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
		},
		fieldID2Name: idToName,
		EntityId:     uint32(entityID),
	}
}

// 将FailedDocRow转换为FailedObjectRow
func convertFailedObjectRows(failedDocRows []*pb.FailedDocRow, fieldID2Name map[uint32]string) ([]*pb.FailedObjectRow, error) {
	var failedObjectRows []*pb.FailedObjectRow

	for _, failedDocRow := range failedDocRows {
		failedObjectRow := &pb.FailedObjectRow{
			ObjectId:   failedDocRow.RowId,
			FailedList: make(map[string]*pb.FailedInfo),
		}

		// 转换失败字段列表（从ID转为字段名）
		for fieldID, failedInfo := range failedDocRow.FailedList {
			fieldName, ok := fieldID2Name[fieldID]
			if !ok {
				fieldName = fmt.Sprintf("field_%d", fieldID) // 使用默认名称
			}
			failedObjectRow.FailedList[fieldName] = failedInfo
		}

		failedObjectRows = append(failedObjectRows, failedObjectRow)
	}

	return failedObjectRows, nil
}

// genUpsertObjectRsp 生成响应
func genUpsertObjectRsp(code pb.EnumErrorCode, msg string) *pb.UpsertObjectRsp {
	return &pb.UpsertObjectRsp{
		RetInfo: &pb.RetInfo{
			Code: code,
			Msg:  msg,
		},
	}
}

// ConvertFieldInfoToFieldValue 将FieldInfo转换为FieldValue
func ConvertFieldInfoToFieldValue(fieldName string, fieldInfo *pb.FieldInfo) *pb.FieldValue {
	if fieldInfo == nil {
		return nil
	}

	fieldValue := &pb.FieldValue{
		FieldType: fieldInfo.FieldType,
	}

	switch fieldInfo.FieldType {
	case pb.EnumFieldType_STR_FIELD, pb.EnumFieldType_INT_FIELD,
		pb.EnumFieldType_FLOAT_FIELD, pb.EnumFieldType_TIME_FIELD:
		fieldValue.SimpleValue = fieldInfo.SimpleValue
	case pb.EnumFieldType_MAP_KV_FIELD:
		// 处理map类型
		if fieldInfo.MapValue != nil {
			fieldValue.MapValue = fieldInfo.MapValue
		}
	case pb.EnumFieldType_SET_FIELD, pb.EnumFieldType_INT_VEC_FIELD:
		// 处理向量类型
		fieldValue.SimpleValue = fieldInfo.SimpleValue
	default:
		// 其他类型
		// TODO: 处理其他字段类型
	}
	return fieldValue
}
