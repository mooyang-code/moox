package logic

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// deleteRows 实现删除行接口（软删除，设置_deleted字段）
//
// 功能说明：
//  1. 根据请求的数据类型（时序数据、静态数据）获取对应的字段配置信息
//  2. 校验请求参数，并根据字段路由配置为删除操作分配存储设备
//  3. 并发请求多个存储设备删除数据，合并结果返回
//
// 参数：
//   - ctx: 上下文
//   - req: 请求参数，包含实体ID、表ID、数据类型、时间区间、行ID等
//
// 返回值：
//   - *pb.DeleteRowsRsp: 响应结果，包含删除的行数
//   - error: 错误信息
func (a *AdapterImpl) deleteRows(ctx context.Context, req *pb.DeleteRowsReq) (*pb.DeleteRowsRsp, error) {
	log.DebugContextf(ctx, "####### Adapter deleteRows : %+v #######", req)
	rsp := &pb.DeleteRowsRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		DeletedCount: 0,
	}

	// 1. 参数校验
	if err := validateDeleteRowsParams(req); err != nil {
		log.ErrorContextf(ctx, "DeleteRows: 参数校验失败: %v", err)
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = err.Error()
		return rsp, nil
	}

	// 2. 准备删除参数及字段与设备映射关系
	params, err := prepareDeleteRowsParams(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "DeleteRows: 准备删除参数失败: %v", err)
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = err.Error()
		return rsp, nil
	}

	if len(params.DeviceList) == 0 {
		log.ErrorContextf(ctx, "没有可用的存储设备配置")
		rsp.RetInfo.Code = pb.EnumErrorCode_NO_ROUTE_STORE_ITEM
		rsp.RetInfo.Msg = "没有配置存储设备"
		return rsp, nil
	}

	// 3. 构建删除任务
	var tasks []deleteTask
	tableID := utils.EscapeTableIDDash(req.GetTableId())
	for _, deviceID := range params.DeviceList {
		// 为每个设备构建删除任务参数（因为我们不知道具体删除哪个设备上的行信息，故全部都删除，即使该设备没有挂载到任何字段）
		deleteParams := &dao.DeleteRowsParams{
			TableID:      tableID,
			DataType:     params.DataType,
			TimeInterval: req.GetTimeInterval(),
			RowIDs:       req.GetRowIds(),
		}
		tasks = append(tasks, deleteTask{
			deviceID:     deviceID,
			TableID:      tableID,
			deviceParams: deleteParams,
		})
	}

	if len(tasks) == 0 {
		log.WarnContextf(ctx, "没有有效的删除任务")
		return rsp, nil
	}

	// 4. 执行删除任务并收集结果
	results, err := executeDeleteTasks(ctx, tasks)
	if err != nil {
		log.ErrorContextf(ctx, "执行删除任务失败: %v", err)
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = fmt.Sprintf("执行删除任务失败: %v", err)
		return rsp, err
	}

	// 5. 处理删除结果
	var totalDeleted uint64
	for _, result := range results {
		if result.err != nil {
			log.WarnContextf(ctx, "设备[%d]删除失败: %v", result.deviceID, result.err)
			continue
		}
		totalDeleted += result.deletedCount
	}

	rsp.DeletedCount = totalDeleted
	return rsp, nil
}

// validateDeleteRowsParams 校验删除行参数
func validateDeleteRowsParams(req *pb.DeleteRowsReq) error {
	if req == nil {
		return fmt.Errorf("请求参数不能为空")
	}
	if req.GetTableId() == "" {
		return fmt.Errorf("表ID不能为空")
	}
	if req.GetDataType() == pb.EnumDataTypeCategory_INVALID_DATA_TYPE_CATEGORY {
		return fmt.Errorf("数据类型不能为空")
	}

	// 检查删除条件：必须指定时间区间或行ID列表中的至少一个
	hasTimeInterval := req.GetTimeInterval() != nil &&
		(req.GetTimeInterval().GetStart() != "" || req.GetTimeInterval().GetEnd() != "")
	hasRowIDs := len(req.GetRowIds()) > 0

	if !hasTimeInterval && !hasRowIDs {
		return fmt.Errorf("必须指定时间区间或行ID列表中的至少一个删除条件")
	}

	return nil
}

// prepareDeleteRowsParams 准备删除行参数
func prepareDeleteRowsParams(ctx context.Context, req *pb.DeleteRowsReq) (*prepareDeleteRowsResult, error) {
	result := &prepareDeleteRowsResult{
		DataType:   req.GetDataType(),
		DeviceList: []int{},
	}

	// 获取所有存储设备
	deviceList, err := cache.GetAllStorageDevices()
	if err != nil {
		return nil, fmt.Errorf("获取存储设备列表失败: %v", err)
	}
	for _, device := range deviceList {
		result.DeviceList = append(result.DeviceList, device.DeviceID)
	}
	return result, nil
}

// prepareDeleteRowsResult 准备删除行参数的结果
type prepareDeleteRowsResult struct {
	DataType   pb.EnumDataTypeCategory // 数据类型
	DeviceList []int                   // 设备ID列表
}

// deleteTask 删除任务结构
type deleteTask struct {
	deviceID     int
	TableID      string
	deviceParams *dao.DeleteRowsParams
}

// deleteResult 删除结果结构
type deleteResult struct {
	deletedCount uint64
	err          error
	deviceID     int
}

// executeDeleteTasks 执行删除任务
func executeDeleteTasks(ctx context.Context, tasks []deleteTask) ([]*deleteResult, error) {
	if len(tasks) == 0 {
		return nil, nil
	}

	// 创建结果通道
	resultChan := make(chan *deleteResult, len(tasks))
	var results []*deleteResult

	// 并发执行任务
	var handlers []func() error
	for _, task := range tasks {
		taskCopy := task
		handlers = append(handlers, func() error {
			// 获取设备
			device, err := dao.NewStoreDevice(ctx, taskCopy.deviceID)
			if err != nil {
				resultChan <- &deleteResult{
					deletedCount: 0,
					err:          fmt.Errorf("获取设备[%d]失败: %v", taskCopy.deviceID, err),
					deviceID:     taskCopy.deviceID,
				}
				return nil // 不返回错误，让其他任务继续执行
			}

			// 执行删除操作
			rsp, err := device.DeleteRows(ctx, taskCopy.deviceParams)
			deletedCount := uint64(0)
			if rsp != nil {
				deletedCount = rsp.GetDeletedCount()
			}

			// 返回结果
			resultChan <- &deleteResult{
				deletedCount: deletedCount,
				err:          err,
				deviceID:     taskCopy.deviceID,
			}
			return nil
		})
	}

	// 等待所有任务完成
	if err := trpc.GoAndWait(handlers...); err != nil {
		return nil, err
	}

	// 收集结果
	close(resultChan)
	for result := range resultChan {
		results = append(results, result)
	}
	return results, nil
}
