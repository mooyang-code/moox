package logic

import (
	"context"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go"
	trpcErrs "trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

// GetFieldInfos 获取字段信息
//
// 功能说明：
//  1. 根据请求的数据类型（时序数据、静态数据）获取对应的字段配置信息
//  2. 校验请求字段ID是否与数据类型匹配，并根据字段路由配置优先级为字段分配存储设备
//  3. 并发请求多个存储设备获取字段数据，合并结果返回
//
// 路由优先级（t_field_route表）：
//   - 高优先级：特定字段配置 (字段ID > 0)
//   - 中优先级：特定数据类型的通用配置 (字段ID = 0, 数据类型匹配)
//   - 低优先级：所有字段所有类型 (字段ID = 0, 数据类型 = 0)
//
// 参数：
//   - ctx: 上下文
//   - req: 请求参数，包含实体ID、表ID、字段ID列表、数据类型等
//
// 返回值：
//   - *pb.GetFieldInfosRsp: 响应结果，包含查询到的字段数据
//   - error: 错误信息
func (a *AdapterImpl) GetFieldInfos(ctx context.Context, req *pb.GetFieldInfosReq) (*pb.GetFieldInfosRsp, error) {
	log.DebugContextf(ctx, "####### Adapter GetFieldInfos : %+v #######", req)

	// 初始化响应
	rsp := a.initializeResponse()

	// 处理参数准备和验证
	params, err := a.handleParameterPreparation(ctx, req, rsp)
	if err != nil {
		return rsp, err
	}
	if rsp.RetInfo.Code != 0 {
		return rsp, nil
	}

	// 验证设备可用性
	if a.validateDeviceAvailability(ctx, params, rsp) {
		return rsp, nil
	}

	// 构建查询任务
	tasks := a.buildQueryTasks(req, params)
	if len(tasks) == 0 {
		log.WarnContextf(ctx, "没有有效的查询任务")
		return rsp, nil
	}

	// 执行查询并处理结果
	allDocRows, queryErr := executeQueryTasks(ctx, tasks)
	err = a.handleQueryErrors(ctx, allDocRows, queryErr, rsp)
	return rsp, err
}

// initializeResponse 初始化响应对象
func (a *AdapterImpl) initializeResponse() *pb.GetFieldInfosRsp {
	return &pb.GetFieldInfosRsp{
		RetInfo:      &pb.RetInfo{},
		FailedFields: make(map[uint32]*pb.FailedInfo),
	}
}

// handleParameterPreparation 处理参数准备和验证
func (a *AdapterImpl) handleParameterPreparation(ctx context.Context,
	req *pb.GetFieldInfosReq, rsp *pb.GetFieldInfosRsp) (*prepareGetFieldInfosParams, error) {
	params, err := prepareGetFieldInfos(ctx, req)
	if err != nil {
		return nil, err
	}
	if params.RetInfo.Code != 0 {
		rsp.RetInfo = params.RetInfo
		return nil, nil
	}

	// 转换失败字段信息并添加到响应中
	for fieldID, errMsg := range params.FailedFields {
		rsp.FailedFields[fieldID] = &pb.FailedInfo{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errMsg,
		}
	}
	return params, nil
}

// validateDeviceAvailability 验证设备可用性
func (a *AdapterImpl) validateDeviceAvailability(ctx context.Context,
	params *prepareGetFieldInfosParams, rsp *pb.GetFieldInfosRsp) bool {
	if len(params.DeviceFieldMap) == 0 {
		log.ErrorContextf(ctx, "没有可用的存储设备配置")
		rsp.RetInfo = &pb.RetInfo{
			Code: pb.EnumErrorCode_NO_ROUTE_STORE_ITEM,
			Msg:  "没有配置存储设备",
		}
		return true // 返回true表示有错误，需要提前返回
	}
	return false // 返回false表示验证通过
}

// buildQueryTasks 构建查询任务
func (a *AdapterImpl) buildQueryTasks(req *pb.GetFieldInfosReq, params *prepareGetFieldInfosParams) []queryTask {
	var tasks []queryTask
	tableID := utils.EscapeTableIDDash(req.GetTableId())
	for deviceID, fieldIDs := range params.DeviceFieldMap {
		getParams := &dao.GetFieldParams{
			TableID:      tableID,
			DataType:     params.DataType,
			TimeInterval: req.GetTimeInterval(),
			RowID:        req.GetRowId(),
			FieldIDs:     fieldIDs,
			MapKeys:      req.GetMapKeys(),
			MaxLimit:     params.MaxLimit,
		}
		tasks = append(tasks, queryTask{
			deviceID:     deviceID,
			TableID:      tableID,
			deviceParams: getParams,
			maxLimit:     params.MaxLimit,
		})
	}
	return tasks
}

// handleQueryErrors 处理查询错误和结果
func (a *AdapterImpl) handleQueryErrors(ctx context.Context,
	allDocRows []*pb.DocRow, queryErr error, rsp *pb.GetFieldInfosRsp) error {
	// 处理设备访问错误
	var devErr *deviceAccessError
	if errors.As(queryErr, &devErr) {
		// 添加失败字段到响应中
		for fieldID, errMsg := range devErr.failedFields {
			rsp.FailedFields[fieldID] = &pb.FailedInfo{
				Code: pb.EnumErrorCode_FAILED_SELECT,
				Msg:  errMsg,
			}
		}
		if len(allDocRows) > 0 {
			rsp.DocRows = allDocRows
			return nil
		}
		rsp.RetInfo = &pb.RetInfo{
			Code: pb.EnumErrorCode_FAILED_SELECT,
			Msg:  fmt.Sprintf("所有设备查询数据失败: %v", devErr.err),
		}
		return devErr.err
	}

	// 其他普通错误处理
	if queryErr != nil && len(allDocRows) == 0 {
		rsp.RetInfo = &pb.RetInfo{
			Code: pb.EnumErrorCode_FAILED_SELECT,
			Msg:  fmt.Sprintf("查询数据失败: %v", queryErr),
		}
		return queryErr
	}

	// 成功情况，设置查询结果
	rsp.DocRows = allDocRows
	return nil
}

// prepareGetFieldInfosParams 准备获取字段信息的参数
type prepareGetFieldInfosParams struct {
	DeviceFieldMap map[int][]uint32        // 存储设备ID到字段ID的映射
	DataType       pb.EnumDataTypeCategory // 数据类型
	RetInfo        *pb.RetInfo             // 返回信息
	FailedFields   map[uint32]string       // 存储失败的字段ID及原因
	MaxLimit       uint32                  // 最大限制
}

// prepareGetFieldInfos 准备获取字段信息
func prepareGetFieldInfos(ctx context.Context, req *pb.GetFieldInfosReq) (*prepareGetFieldInfosParams, error) {
	retInfo := &pb.RetInfo{}
	result := &prepareGetFieldInfosParams{
		RetInfo:        retInfo,
		DeviceFieldMap: make(map[int][]uint32),
		FailedFields:   make(map[uint32]string),
		MaxLimit:       req.GetMaxLimit(), // 获取max_limit参数
	}

	// 检查max_limit参数是否超过允许的最大值(1000)
	if result.MaxLimit > 1000 {
		log.WarnContextf(ctx, "请求的max_limit超过最大允许值1000，将使用1000作为上限")
		result.MaxLimit = 1000
	} else if result.MaxLimit == 0 {
		// 如果未设置，使用默认值
		result.MaxLimit = 100
		log.DebugContextf(ctx, "未设置max_limit，使用默认值: 100")
	}

	// 确定请求的数据类型
	result.DataType = req.GetDataType()
	if !ValidateDataType(result.DataType) {
		log.WarnContextf(ctx, "未知的数据类型: %d", result.DataType)
		retInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		retInfo.Msg = fmt.Sprintf("未知的数据类型: %d", result.DataType)
		return result, nil
	}

	// 1. 从表ID中解析数据集ID
	datasetID, err := utils.ParseDatasetIDFromTableID(req.GetTableId())
	if err != nil {
		log.WarnContextf(ctx, "从表ID解析数据集ID失败: %v", err)
		retInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		retInfo.Msg = fmt.Sprintf("无效的表ID格式: %v", err)
		return result, nil
	}

	// 2. 构建设备到字段的映射
	deviceToFields, unroutedFields, err := BuildFieldMap(ctx, req.GetFieldIds(), int(datasetID))
	if err != nil {
		return nil, err
	}
	result.DeviceFieldMap = deviceToFields

	// 3. 处理无法路由的字段
	for _, fieldID := range unroutedFields {
		result.FailedFields[fieldID] = "没有找到匹配的存储设备"
	}
	return result, nil
}

// 定义查询任务结构
type queryTask struct {
	deviceID     int
	TableID      string
	deviceParams *dao.GetFieldParams
	maxLimit     uint32 // 添加最大限制参数
}

// 查询结果结构
type queryResult struct {
	docRows  []*pb.DocRow
	err      error
	deviceID int      // 设备ID
	fieldIDs []uint32 // 该设备负责的字段ID列表
}

// executeQueryTasks 执行查询任务并收集结果
func executeQueryTasks(ctx context.Context, tasks []queryTask) ([]*pb.DocRow, error) {
	// 并发执行查询
	resultChan := make(chan queryResult, len(tasks))
	defer close(resultChan)

	var handlers []func() error
	for _, task := range tasks {
		taskCopy := task
		handlers = append(handlers, func() error {
			log.DebugContextf(ctx, "执行查询, 设备ID: %d, 表ID: %s, 最大行数限制: %d",
				taskCopy.deviceID, taskCopy.TableID, taskCopy.maxLimit)

			// 执行设备查询
			rows, err := fetchDeviceData(ctx, taskCopy.deviceID, taskCopy.deviceParams)
			resultChan <- queryResult{
				docRows:  rows,
				err:      err,
				deviceID: taskCopy.deviceID,
				fieldIDs: taskCopy.deviceParams.FieldIDs,
			}
			return nil
		})
	}
	_ = trpc.GoAndWait(handlers...)

	// 获取任务中的最大限制值
	var maxLimit uint32
	if len(tasks) > 0 {
		maxLimit = tasks[0].maxLimit
	}

	// 收集并合并结果
	return collectAndMergeResults(ctx, resultChan, len(tasks), maxLimit)
}

// collectAndMergeResults 收集并合并查询结果
func collectAndMergeResults(ctx context.Context, resultChan chan queryResult, taskCount int, maxLimit uint32) ([]*pb.DocRow, error) {
	var firstError error
	docRowsMap := make(map[string]*pb.DocRow)     // 用于按key合并结果
	failedDeviceFields := make(map[uint32]string) // 存储失败设备对应的字段及失败原因

	for range make([]struct{}, taskCount) {
		result := <-resultChan
		if result.err != nil {
			if firstError == nil {
				firstError = result.err
			}
			log.ErrorContextf(ctx, "查询设备[%d]数据失败: %v", result.deviceID, result.err)

			// 记录该设备负责的字段访问失败
			errMsg := fmt.Sprintf("设备[%d]访问失败: %v", result.deviceID, result.err)
			for _, fieldID := range result.fieldIDs {
				failedDeviceFields[fieldID] = errMsg
			}
			continue
		}

		// 合并结果
		for _, docRow := range result.docRows {
			rowKey := docRow.RowId

			// 检查是否已有该键的结果
			if existingRow, exists := docRowsMap[rowKey]; exists {
				// 存在则合并字段
				mergeFields(existingRow, docRow)
			} else {
				// 不存在则添加新记录
				docRowsMap[rowKey] = docRow
			}
		}
	}

	// 将map转换为slice
	var allDocRows []*pb.DocRow
	for _, docRow := range docRowsMap {
		allDocRows = append(allDocRows, docRow)
	}

	// 应用maxLimit限制，如果结果行数超过限制，则截断
	if maxLimit > 0 && uint32(len(allDocRows)) > maxLimit {
		log.DebugContextf(ctx, "结果行数(%d)超过限制(%d)，进行截断", len(allDocRows), maxLimit)
		allDocRows = allDocRows[:maxLimit]
	}

	// 如果有设备访问失败，将失败信息添加到失败字段中
	if len(failedDeviceFields) > 0 {
		// 返回失败字段和错误
		return allDocRows, &deviceAccessError{
			err:          firstError,
			failedFields: failedDeviceFields,
		}
	}
	return allDocRows, firstError
}

// mergeFields 合并两个文档行的字段
func mergeFields(target *pb.DocRow, source *pb.DocRow) {
	if target.Fields == nil {
		target.Fields = make(map[uint32]*pb.FieldInfo)
	}
	for fieldID, fieldInfo := range source.Fields {
		target.Fields[fieldID] = fieldInfo
	}
}

// fetchDeviceData 从指定设备获取数据
func fetchDeviceData(ctx context.Context, deviceID int, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
	// 创建设备对象
	storeDevice, err := dao.NewStoreDevice(ctx, deviceID)
	if err != nil {
		log.ErrorContextf(ctx, "创建存储设备失败[%d], err:%v", deviceID, err)
		return nil, trpcErrs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("创建存储设备失败: %v", err))
	}

	// 调用设备对象的GetFieldInfos函数获取数据
	docRows, err := storeDevice.GetFieldInfos(ctx, params)
	if err != nil {
		log.ErrorContextf(ctx, "设备[%d]查询数据失败: %v", deviceID, err)
		return nil, err
	}
	log.DebugContextf(ctx, "fetchDeviceData 设备[%d]查询数据成功，返回 %d 条数据", deviceID, len(docRows))
	return docRows, nil
}

// deviceAccessError 设备访问错误，包含失败的字段信息
type deviceAccessError struct {
	err          error
	failedFields map[uint32]string
}

// Error 实现error接口
func (e *deviceAccessError) Error() string {
	return e.err.Error()
}
