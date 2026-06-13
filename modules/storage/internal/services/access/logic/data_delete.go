package logic

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// DeleteData 实现删除数据接口（软删除，设置_deleted字段）
func (i *accessorImpl) DeleteData(ctx context.Context, req *pb.DeleteDataReq) (*pb.DeleteDataRsp, error) {
	log.DebugContextf(ctx, "DeleteData: req=%v", req)
	// 1. 校验参数
	if err := validateDeleteDataParams(req); err != nil {
		log.ErrorContextf(ctx, "DeleteData: 参数校验失败: %v", err)
		return genDeleteDataRsp(pb.EnumErrorCode_INVALID_PARAM, err.Error()), nil
	}

	// 2. 准备适配层请求参数
	adapterReq, err := i.prepareDeleteDataAdapterReq(ctx, req)
	if err != nil {
		code := getErrorCode(err)
		log.ErrorContextf(ctx, "DeleteData: 准备适配层请求参数失败: %v", err)
		return genDeleteDataRsp(code, err.Error()), nil
	}

	// 3. 调用适配层服务
	// 创建动态适配层客户端
	adapterClient := CreateDynamicAdapterClient(adapterReq.EntityID)

	// 调用适配层服务
	adapterRsp, err := adapterClient.DeleteRows(ctx, adapterReq.DeleteRowsReq)
	if err != nil {
		log.ErrorContextf(ctx, "DeleteData: 调用适配层服务失败: %v", err)
		return genDeleteDataRsp(pb.EnumErrorCode_INNER_ERR, fmt.Sprintf("调用适配层服务失败: %v", err)), nil
	}

	// 4. 检查适配层响应状态
	if adapterRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
		log.ErrorContextf(ctx, "DeleteData: 适配层返回错误: code=%v, msg=%v",
			adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg)
		return genDeleteDataRsp(adapterRsp.RetInfo.Code, adapterRsp.RetInfo.Msg), nil
	}

	// 5. 组装返回结果
	return &pb.DeleteDataRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
	}, nil
}

// validateDeleteDataParams 校验删除数据参数
func validateDeleteDataParams(req *pb.DeleteDataReq) error {
	if req == nil {
		return fmt.Errorf("请求参数不能为空")
	}
	if req.GetDataKey() == nil {
		return fmt.Errorf("数据键不能为空")
	}
	if req.GetDataKey().GetProjectId() == 0 {
		return fmt.Errorf("项目ID不能为空")
	}
	if req.GetDataKey().GetDatasetId() == 0 {
		return fmt.Errorf("数据集ID不能为空")
	}
	if req.GetDataKey().GetObjectId() == "" {
		return fmt.Errorf("数据对象ID不能为空")
	}
	// 校验ObjectID格式
	if err := ValidateObjectID(req.GetDataKey().GetObjectId()); err != nil {
		return fmt.Errorf("数据对象ID格式无效: %v", err)
	}

	// 检查删除条件：必须指定时间区间或行ID列表中的至少一个
	hasTimeRange := req.GetTimeRange() != nil &&
		(req.GetTimeRange().GetStart() != "" ||
			req.GetTimeRange().GetEnd() != "")
	hasRowIDs := len(req.GetRowIds()) > 0

	if !hasTimeRange && !hasRowIDs {
		return fmt.Errorf("必须指定时间区间或行ID列表中的至少一个删除条件")
	}
	return nil
}

// prepareDeleteDataAdapterReq 准备删除数据的适配层请求参数
func (i *accessorImpl) prepareDeleteDataAdapterReq(ctx context.Context, req *pb.DeleteDataReq) (*DeleteDataAdapterReq, error) {
	// 1. 获取数据集信息
	datasetInfo, err := cache.GetDatasetByID(int(req.GetDataKey().GetDatasetId()))
	if err != nil {
		return nil, fmt.Errorf("获取数据集信息失败: %v", err)
	}
	if datasetInfo == nil {
		return nil, fmt.Errorf("数据集[%d]不存在", req.GetDataKey().GetDatasetId())
	}

	// 2. 获取数据对象路由信息
	objectRoute, err := cache.GetObjectRouteByDatasetAndObject(
		int(req.GetDataKey().GetDatasetId()),
		req.GetDataKey().GetObjectId())
	if err != nil {
		return nil, fmt.Errorf("获取数据对象路由信息失败: %v", err)
	}
	if objectRoute == nil {
		return nil, fmt.Errorf("数据对象[%s]在数据集[%d]中不存在路由配置",
			req.GetDataKey().GetObjectId(), req.GetDataKey().GetDatasetId())
	}

	// 3. 构建表ID
	tableID := utils.GenDataTableID(req.GetDataKey().GetDatasetId(), req.GetDataKey().GetObjectId(),
		req.GetDataKey().GetFreq())

	// 4. 转换时间区间
	var timeInterval *pb.TimeInterval
	if req.GetTimeRange() != nil {
		timeInterval = &pb.TimeInterval{
			Start: req.GetTimeRange().GetStart(),
			End:   req.GetTimeRange().GetEnd(),
		}
	}

	// 5. 获取行ID列表
	rowIDs := req.GetRowIds()

	// 6. 构建适配层删除请求
	return &DeleteDataAdapterReq{
		EntityID: objectRoute.EntityID,
		DeleteRowsReq: &pb.DeleteRowsReq{
			TableId:      tableID,
			DataType:     pb.EnumDataTypeCategory(datasetInfo.DataType),
			TimeInterval: timeInterval,
			RowIds:       rowIDs,
		},
	}, nil
}

// DeleteDataAdapterReq 删除数据的适配层请求结构
type DeleteDataAdapterReq struct {
	EntityID      int               // 存储实体ID
	DeleteRowsReq *pb.DeleteRowsReq // 适配层删除请求
}

// genDeleteDataRsp 生成删除数据响应
func genDeleteDataRsp(code pb.EnumErrorCode, msg string) *pb.DeleteDataRsp {
	return &pb.DeleteDataRsp{
		RetInfo: &pb.RetInfo{
			Code: code,
			Msg:  msg,
		},
	}
}
