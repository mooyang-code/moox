package logic

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// DeleteObject 实现删除数据对象接口（软删除，设置_deleted字段）
func (i *accessorImpl) DeleteObject(ctx context.Context, req *pb.DeleteObjectReq) (*pb.DeleteObjectRsp, error) {
	log.DebugContextf(ctx, "DeleteObject: req=%v", req)
	// 1. 校验参数
	if err := validateDeleteObjectParams(req); err != nil {
		log.ErrorContextf(ctx, "DeleteObject: 参数校验失败: %v", err)
		return genDeleteObjectRsp(pb.EnumErrorCode_INVALID_PARAM, err.Error()), nil
	}

	// 2. 准备适配层请求参数（按存储实体分组）
	adapterReqs, err := i.prepareDeleteObjectAdapterReqs(ctx, req)
	if err != nil {
		code := getErrorCode(err)
		log.ErrorContextf(ctx, "DeleteObject: 准备适配层请求参数失败: %v", err)
		return genDeleteObjectRsp(code, err.Error()), nil
	}

	// 3. 调用适配层服务（可能需要调用多个存储实体）
	var totalDeletedCount uint64
	for _, adapterReq := range adapterReqs {
		// 创建动态适配层客户端
		adapterClient := CreateDynamicAdapterClient(int(adapterReq.EntityID))

		// 调用适配层服务
		adapterRsp, err := adapterClient.DeleteRows(ctx, adapterReq.DeleteRowsReq)
		if err != nil {
			log.ErrorContextf(ctx, "DeleteObject: 调用存储实体[%d]适配层服务失败: %v", adapterReq.EntityID, err)
			errMsg := fmt.Sprintf("调用存储实体[%d]适配层服务失败: %v", adapterReq.EntityID, err)
			return genDeleteObjectRsp(pb.EnumErrorCode_INNER_ERR, errMsg), nil
		}

		// 4. 检查适配层响应状态
		if adapterRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "DeleteObject: 存储实体[%d]适配层返回错误: code=%v, msg=%v",
				adapterReq.EntityID, adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg)
			return genDeleteObjectRsp(adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg), nil
		}

		totalDeletedCount += adapterRsp.GetDeletedCount()
		log.InfoContextf(ctx, "DeleteObject: 存储实体[%d]删除成功，删除对象数: %d", adapterReq.EntityID, adapterRsp.GetDeletedCount())
	}

	// 5. 组装返回结果
	log.InfoContextf(ctx, "DeleteObject: 删除完成，总共删除对象数: %d", totalDeletedCount)
	return &pb.DeleteObjectRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  fmt.Sprintf("删除成功，共删除%d个对象", totalDeletedCount),
		},
	}, nil
}

// validateDeleteObjectParams 校验删除数据对象参数
func validateDeleteObjectParams(req *pb.DeleteObjectReq) error {
	if req == nil {
		return fmt.Errorf("请求参数不能为空")
	}
	if req.GetProjectId() == 0 {
		return fmt.Errorf("项目ID不能为空")
	}
	if req.GetDatasetId() == 0 {
		return fmt.Errorf("数据集ID不能为空")
	}

	// 检查删除条件：必须指定对象ID列表
	objectIDs := req.GetObjectIds()
	if len(objectIDs) == 0 {
		return fmt.Errorf("删除操作必须指定具体的对象ID列表，不允许批量删除所有数据")
	}

	// 检查对象ID是否为空字符串并校验格式
	for i, objectID := range objectIDs {
		if objectID == "" {
			return fmt.Errorf("第%d个对象ID不能为空字符串", i+1)
		}
		// 校验ObjectID格式
		if err := ValidateObjectID(objectID); err != nil {
			return fmt.Errorf("第%d个对象ID格式无效: %v", i+1, err)
		}
	}

	return nil
}

// prepareDeleteObjectAdapterReqs 准备删除数据对象的适配层请求参数（按存储实体分组）
func (i *accessorImpl) prepareDeleteObjectAdapterReqs(ctx context.Context, req *pb.DeleteObjectReq) ([]*DeleteObjectAdapterReq, error) {
	// 1. 获取数据集信息
	datasetInfo, err := cache.GetDatasetByID(int(req.GetDatasetId()))
	if err != nil {
		return nil, fmt.Errorf("获取数据集信息失败: %v", err)
	}
	if datasetInfo == nil {
		return nil, fmt.Errorf("数据集[%d]不存在", req.GetDatasetId())
	}

	// 2. 按存储实体分组对象ID
	entityObjectMap := make(map[int][]string) // entityID -> objectIDs

	for _, objectID := range req.GetObjectIds() {
		// 获取每个对象的路由信息
		objectRoute, err := cache.GetObjectRouteByDatasetAndObject(
			int(req.GetDatasetId()),
			objectID)
		if err != nil {
			return nil, fmt.Errorf("获取数据对象[%s]路由信息失败: %v", objectID, err)
		}
		if objectRoute == nil {
			return nil, fmt.Errorf("数据对象[%s]在数据集[%d]中不存在路由配置", objectID, req.GetDatasetId())
		}

		// 按存储实体分组
		entityID := objectRoute.EntityID
		if entityObjectMap[entityID] == nil {
			entityObjectMap[entityID] = make([]string, 0)
		}
		entityObjectMap[entityID] = append(entityObjectMap[entityID], objectID)
	}

	// 3. 为每个存储实体构建删除请求
	var adapterReqs []*DeleteObjectAdapterReq
	tableID := fmt.Sprintf("dataset_%d", req.GetDatasetId())

	for entityID, objectIDs := range entityObjectMap {
		// 构建适配层删除请求
		deleteRowsReq := &pb.DeleteRowsReq{
			TableId:  tableID,
			DataType: pb.EnumDataTypeCategory_STATIC_DATA_TYPE, // 数据对象通常是静态数据
			RowIds:   objectIDs,                                // 直接使用对象ID作为行ID
		}

		adapterReqs = append(adapterReqs, &DeleteObjectAdapterReq{
			EntityID:      uint32(entityID),
			DeleteRowsReq: deleteRowsReq,
		})
		log.InfoContextf(ctx, "准备删除存储实体[%d]中的对象: %v", entityID, objectIDs)
	}
	return adapterReqs, nil
}

// DeleteObjectAdapterReq 删除数据对象的适配层请求结构
type DeleteObjectAdapterReq struct {
	DeleteRowsReq *pb.DeleteRowsReq // 适配层删除请求
	EntityID      uint32            // 存储实体ID
}

// genDeleteObjectRsp 生成删除数据对象响应
func genDeleteObjectRsp(code pb.EnumErrorCode, msg string) *pb.DeleteObjectRsp {
	return &pb.DeleteObjectRsp{
		RetInfo: &pb.RetInfo{
			Code: code,
			Msg:  msg,
		},
	}
}
