package logic

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// FetchObject 实现拉取数据对象接口
func (i *accessorImpl) FetchObject(ctx context.Context, req *pb.FetchObjectReq) (*pb.FetchObjectRsp, error) {
	log.DebugContextf(ctx, "FetchObject: req=%v", req)
	// 1. 校验参数
	if err := validateFetchObjectParams(req); err != nil {
		log.ErrorContextf(ctx, "FetchObject: 参数校验失败: %v", err)
		return genFetchObjectRsp(pb.EnumErrorCode_INVALID_PARAM, err.Error()), nil
	}

	// 2. 准备适配层请求参数
	adapterReq, err := i.prepareFetchObjectAdapterReq(ctx, req)
	if err != nil {
		code := getErrorCode(err)
		log.ErrorContextf(ctx, "FetchObject: 准备适配层请求参数失败: %v", err)
		return genFetchObjectRsp(code, err.Error()), nil
	}

	// 3. 调用适配层服务
	adapterRsp, err := i.callAdapterGetFieldInfos(ctx, adapterReq.EntityId, adapterReq.GetFieldInfosReq)
	if err != nil {
		log.ErrorContextf(ctx, "FetchObject: 调用适配层服务失败: %v", err)
		return genFetchObjectRsp(pb.EnumErrorCode_INNER_ERR, fmt.Sprintf("调用适配层服务失败: %v", err)), nil
	}

	// 4. 检查适配层响应状态
	if adapterRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
		log.ErrorContextf(ctx, "FetchObject: 适配层返回错误: code=%v, msg=%v",
			adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg)
		return genFetchObjectRsp(adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg), nil
	}

	// 5. 转换响应结果
	objectRows, err := convertToObjectRows(ctx, adapterRsp.DocRows, adapterReq.fieldID2Name)
	if err != nil {
		log.ErrorContextf(ctx, "FetchObject: 转换结果失败: %v", err)
		return genFetchObjectRsp(pb.EnumErrorCode_INNER_ERR, "转换结果失败"), nil
	}

	// 6. 组装返回结果
	return &pb.FetchObjectRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		ObjectRows: objectRows,
	}, nil
}

// validateFetchObjectParams 验证请求参数
func validateFetchObjectParams(req *pb.FetchObjectReq) error {
	if req.AuthInfo == nil {
		return fmt.Errorf("auth_info is nil")
	}
	if req.ProjectId <= 0 {
		return fmt.Errorf("invalid project_id: %d", req.ProjectId)
	}
	if req.DatasetId <= 0 {
		return fmt.Errorf("invalid dataset_id: %d", req.DatasetId)
	}
	return nil
}

// GetFieldInfosAdapterReq 用于构建适配层请求的扩展结构
type GetFieldInfosAdapterReq struct {
	*pb.GetFieldInfosReq
	EntityId     uint32
	fieldID2Name map[uint32]string // 用于保存字段ID到字段名的映射
}

// prepareFetchObjectAdapterReq 准备适配层请求
func (i *accessorImpl) prepareFetchObjectAdapterReq(ctx context.Context, req *pb.FetchObjectReq) (*GetFieldInfosAdapterReq, error) {
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

	// 3. 将字段名转换为字段ID（我们不管FieldKeys为空的情况，为空fieldIDs亦为空，底层服务会拉取所有字段）
	fieldIDs, fieldID2Name, err := convertNamesToIDs(req.FieldKeys, metaFields)
	if err != nil {
		return nil, fmt.Errorf("转换字段名称失败: %v", err)
	}

	// 4. 转换MapKeys
	mapKeys, err := convertMapKeys(req.MapKeys, fieldID2Name)
	if err != nil {
		return nil, fmt.Errorf("转换Map字段键失败: %v", err)
	}

	// 5. 获取dataset的存储路由
	entityID, err := GetDataRoute(req.DatasetId, "*")
	if err != nil {
		return nil, fmt.Errorf("未找到默认对象路由")
	}

	// 6. 构建适配层请求
	adapterReq := &GetFieldInfosAdapterReq{
		GetFieldInfosReq: &pb.GetFieldInfosReq{
			TableId:      tableID,
			DataType:     pb.EnumDataTypeCategory_STATIC_DATA_TYPE, // 数据对象，都是静态数据。
			FieldIds:     fieldIDs,
			MapKeys:      mapKeys,
			TimeInterval: nil, // 静态数据不需要时间区间
		},
		fieldID2Name: fieldID2Name,
		EntityId:     uint32(entityID),
	}
	return adapterReq, nil
}

// getErrorCode 根据错误类型返回对应的错误码
func getErrorCode(err error) pb.EnumErrorCode {
	if err == nil {
		return pb.EnumErrorCode_SUCCESS
	}

	errMsg := err.Error()
	switch {
	case errMsg == "auth_info is nil":
		return pb.EnumErrorCode_INNER_ERR
	case errMsg == "未找到默认对象路由":
		return pb.EnumErrorCode_NO_ROUTE_STORE_ITEM
	case errMsg == "未找到数据集的元数据字段":
		return pb.EnumErrorCode_INVALID_PARAM // 使用已定义的错误码
	default:
		return pb.EnumErrorCode_INNER_ERR
	}
}

// callAdapterGetFieldInfos 调用适配层GetFieldInfos服务
func (i *accessorImpl) callAdapterGetFieldInfos(ctx context.Context, entityId uint32,
	req *pb.GetFieldInfosReq) (*pb.GetFieldInfosRsp, error) {
	// 创建动态适配层客户端
	adapterClient := CreateDynamicAdapterClient(int(entityId))

	// 调用适配层服务
	rsp, err := adapterClient.GetFieldInfos(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "调用适配层GetFieldInfos服务失败: %v", err)
		return nil, err
	}
	return rsp, nil
}

// 将DocRows转换为ObjectRows
func convertToObjectRows(ctx context.Context, docRows []*pb.DocRow, fieldID2Name map[uint32]string) ([]*pb.ObjectRow, error) {
	var objectRows []*pb.ObjectRow
	for _, docRow := range docRows {
		objectRow := &pb.ObjectRow{
			ObjectId: docRow.RowId, // 使用RowId作为对象ID
			Fields:   make(map[string]*pb.FieldValue),
		}

		// 转换字段
		for fieldID, fieldInfo := range docRow.Fields {
			fieldName, ok := fieldID2Name[fieldID]
			if !ok { // 如果找不到对应的字段名，跳过(可能底层返回了废弃的字段ID)
				continue
			}

			// 转换字段值
			fieldValue, err := convertFieldValue(fieldName, fieldInfo)
			if err != nil {
				log.WarnContextf(ctx, "转换字段值失败: %v", err)
				continue
			}
			objectRow.Fields[fieldName] = fieldValue
		}
		objectRows = append(objectRows, objectRow)
	}
	return objectRows, nil
}

// genFetchObjectRsp 生成响应
func genFetchObjectRsp(code pb.EnumErrorCode, msg string) *pb.FetchObjectRsp {
	return &pb.FetchObjectRsp{
		RetInfo: &pb.RetInfo{
			Code: code,
			Msg:  msg,
		},
	}
}
