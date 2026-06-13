package logic

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// QueryObject 实现查询数据对象接口
func (i *accessorImpl) QueryObject(ctx context.Context, req *pb.QueryObjectReq) (*pb.QueryObjectRsp, error) {
	log.DebugContextf(ctx, "QueryObject: req=%v", req)
	// 1. 校验参数
	if err := validateQueryObjectParams(req); err != nil {
		log.ErrorContextf(ctx, "QueryObject: 参数校验失败: %v", err)
		return genQueryObjectRsp(pb.EnumErrorCode_INVALID_PARAM, err.Error()), nil
	}

	// 2. 准备适配层请求参数
	adapterReq, err := i.prepareQueryObjectAdapterReq(ctx, req)
	if err != nil {
		code := getErrorCode(err)
		log.ErrorContextf(ctx, "QueryObject: 准备适配层请求参数失败: %v", err)
		return genQueryObjectRsp(code, err.Error()), nil
	}

	// 3. 调用适配层服务
	// 创建动态适配层客户端
	adapterClient := CreateDynamicAdapterClient(int(adapterReq.EntityId))

	// 调用适配层服务
	adapterRsp, err := adapterClient.SearchFieldInfos(ctx, adapterReq.SearchFieldInfosReq)
	if err != nil {
		log.ErrorContextf(ctx, "QueryObject: 调用适配层服务失败: %v", err)
		return genQueryObjectRsp(pb.EnumErrorCode_INNER_ERR, fmt.Sprintf("调用适配层服务失败: %v", err)), nil
	}

	// 4. 检查适配层响应状态
	if adapterRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
		log.ErrorContextf(ctx, "QueryObject: 适配层返回错误: code=%v, msg=%v",
			adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg)
		return genQueryObjectRsp(adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg), nil
	}

	// 5. 转换响应结果
	objectRows, err := convertToObjectRows(ctx, adapterRsp.DocRows, adapterReq.fieldID2Name)
	if err != nil {
		log.ErrorContextf(ctx, "QueryObject: 转换结果失败: %v", err)
		return genQueryObjectRsp(pb.EnumErrorCode_INNER_ERR, "转换结果失败"), nil
	}

	// 6. 转换失败的字段信息
	failedFields := make(map[string]*pb.FailedInfo)
	for fieldID, failedInfo := range adapterRsp.FailedFields {
		fieldName, ok := adapterReq.fieldID2Name[fieldID]
		if !ok {
			fieldName = fmt.Sprintf("field_%d", fieldID) // 使用默认名称
		}
		failedFields[fieldName] = failedInfo
	}

	// 7. 组装返回结果
	return &pb.QueryObjectRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		Total:        adapterRsp.Total,
		ObjectRows:   objectRows,
		FailedFields: failedFields,
	}, nil
}

// validateQueryObjectParams 验证请求参数
func validateQueryObjectParams(req *pb.QueryObjectReq) error {
	if req.AuthInfo == nil {
		return fmt.Errorf("auth_info is nil")
	}
	if req.ProjectId <= 0 {
		return fmt.Errorf("invalid project_id: %d", req.ProjectId)
	}
	if req.DatasetId <= 0 {
		return fmt.Errorf("invalid dataset_id: %d", req.DatasetId)
	}
	// 移除对options为nil的检查，允许用户不传options参数来获取全部数据
	return nil
}

// SearchFieldInfosAdapterReq 用于构建适配层请求的扩展结构
type SearchFieldInfosAdapterReq struct {
	*pb.SearchFieldInfosReq
	fieldID2Name map[uint32]string // 用于保存字段ID到字段名的映射
	EntityId     uint32
}

// prepareQueryObjectAdapterReq 准备适配层请求
func (i *accessorImpl) prepareQueryObjectAdapterReq(ctx context.Context, req *pb.QueryObjectReq) (*SearchFieldInfosAdapterReq, error) {
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

	// 3. 使用公共函数构建字段映射
	nameToID, idToName, _ := BuildFieldMappings(metaFields, false)

	// 4. 转换查询条件（将字段名转换为字段ID）
	searchOptions, err := convertToSearchOptions(req.Options, nameToID)
	if err != nil {
		return nil, fmt.Errorf("转换查询条件失败: %v", err)
	}

	// 5. 获取dataset的默认存储路由（数据对象不再分表，默认路由的信息即为其存储实例）
	entityID, err := GetDataRoute(req.DatasetId, "*")
	if err != nil {
		return nil, fmt.Errorf("未找到默认对象路由")
	}

	// 6. 准备页信息
	pageInfo := &pb.PageInfo{
		PageIdx: 1,  // 默认第一页
		Size:    50, // 默认每页50条
	}
	if req.PageInfo != nil {
		pageInfo.PageIdx = req.PageInfo.PageIdx
		pageInfo.Size = req.PageInfo.Size
	}

	// 7. 构建适配层请求
	adapterReq := &SearchFieldInfosAdapterReq{
		SearchFieldInfosReq: &pb.SearchFieldInfosReq{
			TableId:       tableID,
			DataType:      pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
			SearchOptions: searchOptions,
			PageInfo:      pageInfo,
			TimeInterval:  nil, // 静态数据不需要时间区间
		},
		fieldID2Name: idToName,
		EntityId:     uint32(entityID),
	}
	return adapterReq, nil
}

// convertToSearchOptions 将Options转换为SearchOptions
func convertToSearchOptions(options *pb.Options, nameToID map[string]uint32) (*pb.SearchOptions, error) {
	// 当options为nil时，返回一个空的SearchOptions对象，表示返回全部数据
	if options == nil {
		return &pb.SearchOptions{
			Logical: pb.Logical_LogicalAnd, // 默认逻辑关系
			MaxNum:  0,                     // 0表示不限制数量
		}, nil
	}

	result := &pb.SearchOptions{
		Logical:        options.Logical,
		MaxNum:         options.MaxNum,
		CondGroups:     make([]*pb.SearchCondGroup, 0, len(options.CondGroups)),
		Sort:           make([]*pb.SearchSort, 0, len(options.Sort)),
		ReturnFieldIds: make([]uint32, 0, len(options.Includes)),
	}

	// 转换条件组
	for _, condGroup := range options.CondGroups {
		newCondGroup := &pb.SearchCondGroup{
			Logical: condGroup.Logical,
			Conds:   make([]*pb.SearchCond, 0, len(condGroup.Conds)),
		}

		for _, cond := range condGroup.Conds {
			// 字段名转ID
			fieldID, ok := nameToID[cond.FieldKey]
			if !ok {
				return nil, fmt.Errorf("未找到字段: %s", cond.FieldKey)
			}

			newCond := &pb.SearchCond{
				FieldId: fieldID,
				Op:      cond.Op,
				Value:   cond.Value,
				MapKey:  cond.MapKey,
			}
			newCondGroup.Conds = append(newCondGroup.Conds, newCond)
		}
		result.CondGroups = append(result.CondGroups, newCondGroup)
	}

	// 转换排序信息
	for _, sortInfo := range options.Sort {
		// 字段名转ID
		fieldID, ok := nameToID[sortInfo.FieldKey]
		if !ok {
			return nil, fmt.Errorf("未找到排序字段: %s", sortInfo.FieldKey)
		}

		newSortInfo := &pb.SearchSort{
			FieldId: fieldID,
			Sort:    sortInfo.Sort,
			MapKey:  sortInfo.MapKey,
		}
		result.Sort = append(result.Sort, newSortInfo)
	}

	// 转换包含的字段
	for _, fieldName := range options.Includes {
		fieldID, ok := nameToID[fieldName]
		if !ok {
			return nil, fmt.Errorf("未找到包含的字段: %s", fieldName)
		}
		result.ReturnFieldIds = append(result.ReturnFieldIds, fieldID)
	}

	return result, nil
}

// genQueryObjectRsp 生成响应
func genQueryObjectRsp(code pb.EnumErrorCode, msg string) *pb.QueryObjectRsp {
	return &pb.QueryObjectRsp{
		RetInfo: &pb.RetInfo{
			Code: code,
			Msg:  msg,
		},
	}
}
