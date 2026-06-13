package logic

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// SetFieldInfos 设置字段信息
//
// 功能说明：
//  1. 根据请求的数据类型（时序数据、静态数据）获取对应的字段配置信息
//  2. 校验请求字段ID是否与数据类型匹配，并根据字段路由配置优先级为字段分配存储设备
//  3. 并发请求多个存储设备set字段数据，合并结果返回
//
// 路由优先级（t_field_route表）：
//   - 高优先级：特定字段配置 (字段ID > 0)
//   - 中优先级：特定数据类型的通用配置 (字段ID = 0, 数据类型匹配)
//   - 低优先级：所有字段所有类型 (字段ID = 0, 数据类型 = 0)
//
// 参数：
//   - ctx: 上下文
//   - req: 请求参数，包含实体ID、表ID、更新字段数据、数据类型等
//
// 返回值：
//   - *pb.SetFieldInfosRsp: 响应结果，包含字段修改信息、历史行数据和失败记录
//   - error: 错误信息
func (a *AdapterImpl) SetFieldInfos(ctx context.Context, req *pb.SetFieldInfosReq) (*pb.SetFieldInfosRsp, error) {
	req.TableId = utils.EscapeTableIDDash(req.GetTableId())
	log.DebugContextf(ctx, "####### Adapter SetFieldInfos : %+v #######", req)
	rsp := &pb.SetFieldInfosRsp{
		RetInfo:     &pb.RetInfo{},
		ModifyInfos: []*pb.ModifyFieldInfo{},
		FailedRows:  []*pb.FailedDocRow{},
	}

	// 准备参数及字段与设备映射关系
	params, err := a.prepareSetParams(ctx, req, rsp)
	if err != nil {
		return rsp, err
	}
	if rsp.RetInfo.Code != 0 {
		return rsp, nil
	}

	// 构建并执行设置任务
	tasks := a.buildSetTasks(req, params)
	if len(tasks) == 0 {
		log.WarnContextf(ctx, "没有有效的更新任务")
		return rsp, nil
	}

	// 执行任务并处理结果
	err = a.executeAndProcessTasks(ctx, tasks, rsp)
	return rsp, err
}

// prepareSetFieldInfosParams 用于准备字段设置参数
type prepareSetFieldInfosParams struct {
	RetInfo        *pb.RetInfo             // 返回信息
	DataType       pb.EnumDataTypeCategory // 数据类型
	DeviceFieldMap map[int][]uint32        // 设备ID与字段ID的映射关系
	FailedDocRows  []*pb.FailedDocRow      // 失败的行信息
}

// setTask 设置任务结构
type setTask struct {
	deviceID     int
	TableID      string
	deviceParams *dao.SetFieldParams
}

// setResult 设置结果结构
type setResult struct {
	modifyInfos []*pb.ModifyFieldInfo
	LastRows    []*pb.DocRow
	failedRows  []*pb.FailedDocRow
	err         error
	deviceID    int
}

// prepareSetFieldInfos 准备设置字段的参数
func prepareSetFieldInfos(ctx context.Context, req *pb.SetFieldInfosReq) (*prepareSetFieldInfosParams, error) {
	retInfo := &pb.RetInfo{}
	result := &prepareSetFieldInfosParams{
		RetInfo:        retInfo,
		DeviceFieldMap: make(map[int][]uint32),
		FailedDocRows:  []*pb.FailedDocRow{},
	}

	// 确定请求的数据类型
	result.DataType = req.GetDataType()
	if !ValidateDataType(result.DataType) {
		log.WarnContextf(ctx, "未知的数据类型: %d", result.DataType)
		retInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		retInfo.Msg = fmt.Sprintf("未知的数据类型: %d", result.DataType)
		return result, nil
	}

	// 收集所有字段ID
	allFieldIDs := collectFieldIDs(req)

	// 从表ID中解析数据集ID
	datasetID, err := utils.ParseDatasetIDFromTableID(req.GetTableId())
	if err != nil {
		log.WarnContextf(ctx, "从表ID解析数据集ID失败: %v", err)
		result.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		result.RetInfo.Msg = fmt.Sprintf("无效的表ID格式: %v", err)
		return result, nil
	}

	// 构建设备到字段的映射
	deviceToFields, unroutedFields, err := BuildFieldMap(ctx, allFieldIDs, int(datasetID))
	if err != nil {
		return nil, err
	}
	result.DeviceFieldMap = deviceToFields

	// 处理无法路由的字段
	handleUnroutedFields(req, unroutedFields, result)
	return result, nil
}

// collectFieldIDs 收集请求中的所有字段ID
func collectFieldIDs(req *pb.SetFieldInfosReq) []uint32 {
	fieldIDMap := make(map[uint32]bool)
	for _, updateRow := range req.GetUpdateDocRows() {
		for fieldID := range updateRow.GetFields() {
			fieldIDMap[fieldID] = true
		}
	}

	var allFieldIDs []uint32
	for fieldID := range fieldIDMap {
		allFieldIDs = append(allFieldIDs, fieldID)
	}
	return allFieldIDs
}

// handleUnroutedFields 处理无法路由的字段
func handleUnroutedFields(req *pb.SetFieldInfosReq, unroutedFields []uint32,
	result *prepareSetFieldInfosParams) {
	if len(unroutedFields) == 0 {
		return
	}

	failedRowMap := make(map[string]*pb.FailedDocRow)

	// 添加字段失败信息的辅助函数
	addFailedField := func(fieldID uint32, code pb.EnumErrorCode, errMsg string) {
		for _, updateRow := range req.GetUpdateDocRows() {
			if _, exists := updateRow.GetFields()[fieldID]; exists {
				failedRow, ok := failedRowMap[updateRow.GetRowId()]
				if !ok {
					failedRow = &pb.FailedDocRow{
						Times:      updateRow.GetTimes(),
						RowId:      updateRow.GetRowId(),
						FailedList: make(map[uint32]*pb.FailedInfo),
					}
					failedRowMap[updateRow.GetRowId()] = failedRow
				}
				failedRow.FailedList[fieldID] = &pb.FailedInfo{
					Code: code,
					Msg:  errMsg,
				}
			}
		}
	}

	// 处理无法路由的字段
	for _, fieldID := range unroutedFields {
		addFailedField(fieldID, pb.EnumErrorCode_NO_ROUTE_STORE_ITEM, "没有找到匹配的存储设备")
	}

	// 添加失败的行到结果中，避免重复
	for _, failedRow := range failedRowMap {
		found := false
		for _, existingRow := range result.FailedDocRows {
			if existingRow.RowId == failedRow.RowId {
				found = true
				break
			}
		}
		if !found {
			result.FailedDocRows = append(result.FailedDocRows, failedRow)
		}
	}
}

// executeSetTasks 执行设置任务
func executeSetTasks(ctx context.Context, tasks []setTask) ([]*setResult, error) {
	// 创建结果通道
	resultChan := make(chan *setResult, len(tasks))
	defer close(resultChan)

	// 创建错误通道
	errChan := make(chan error, 1)
	defer close(errChan)

	// 并发执行任务
	var handlers []func() error
	for _, task := range tasks {
		taskCopy := task
		handlers = append(handlers, func() error {
			// 获取设备
			device, err := dao.NewStoreDevice(ctx, taskCopy.deviceID)
			if err != nil {
				return fmt.Errorf("获取设备[%d]失败: %v", taskCopy.deviceID, err)
			}

			// 执行设置操作（只要有失败就返回）
			rsp, err := device.SetFieldInfos(ctx, taskCopy.deviceParams)

			// 返回结果
			resultChan <- &setResult{
				modifyInfos: rsp.GetModifyInfos(),
				LastRows:    rsp.GetLastRows(),
				failedRows:  rsp.GetFailedRows(),
				deviceID:    taskCopy.deviceID,
				err:         err,
			}
			if err != nil {
				return fmt.Errorf("设备[%d]更新失败: %v", taskCopy.deviceID, err)
			}
			return nil
		})
	}

	// 等待所有任务完成
	if err := trpc.GoAndWait(handlers...); err != nil {
		return nil, err
	}

	// 收集结果
	var results []*setResult
	for range tasks {
		result := <-resultChan
		results = append(results, result)
	}
	return results, nil
}

// filterUpdateDocRowsByFields 根据字段ID过滤更新行
func filterUpdateDocRowsByFields(updateRows []*pb.UpdateDocRow, fieldIDs []uint32) []*pb.UpdateDocRow {
	if len(fieldIDs) == 0 {
		return nil
	}

	// 创建字段ID集合，用于快速查找
	fieldIDSet := make(map[uint32]bool)
	for _, fieldID := range fieldIDs {
		fieldIDSet[fieldID] = true
	}

	var result []*pb.UpdateDocRow
	for _, row := range updateRows {
		filteredFields := make(map[uint32]*pb.UpdateFieldInfo)

		for fieldID, updateInfo := range row.GetFields() {
			if fieldIDSet[fieldID] {
				filteredFields[fieldID] = updateInfo
			}
		}

		if len(filteredFields) > 0 {
			// 创建新的UpdateDocRow，只包含指定字段
			filteredRow := &pb.UpdateDocRow{
				Times:  row.GetTimes(),
				RowId:  row.GetRowId(),
				Fields: filteredFields,
			}
			result = append(result, filteredRow)
		}
	}
	return result
}

// getHistoricalData 获取历史数据
func (a *AdapterImpl) getHistoricalData(ctx context.Context, req *pb.SetFieldInfosReq) ([]*pb.DocRow, error) {
	// 创建GetFieldInfos请求
	getReq := &pb.GetFieldInfosReq{
		TableId:  utils.EscapeTableIDDash(req.GetTableId()),
		DataType: req.GetDataType(),
		FieldIds: []uint32{}, // 获取所有字段
		TimeInterval: &pb.TimeInterval{
			// 根据历史行数限制设置时间范围
			Start: "", // 留空，表示不限制开始时间
			End:   "", // 留空，表示当前时间
		},
	}

	// 调用GetFieldInfos接口
	getResp, err := a.GetFieldInfos(ctx, getReq)
	if err != nil {
		log.WarnContextf(ctx, "获取历史数据失败: %v", err)
		return nil, err
	}

	// 只返回请求的行数
	if uint32(len(getResp.GetDocRows())) > req.GetHistoricalRowsLimit() {
		return getResp.GetDocRows()[:req.GetHistoricalRowsLimit()], nil
	}
	return getResp.GetDocRows(), nil
}

// prepareSetParams 准备设置参数
func (a *AdapterImpl) prepareSetParams(ctx context.Context, req *pb.SetFieldInfosReq, rsp *pb.SetFieldInfosRsp) (*prepareSetFieldInfosParams, error) {
	// 检查UpdateDocRows行数限制
	updateRowsCount := uint32(len(req.GetUpdateDocRows()))
	if updateRowsCount > 0 {
		// 获取配置中的最大行数限制
		cfg := config.GetGlobalConfig()
		maxUpdateRows := uint32(25) // 默认值
		if cfg != nil && cfg.Limits.MaxUpdateRows > 0 {
			maxUpdateRows = cfg.Limits.MaxUpdateRows
		}

		if updateRowsCount > maxUpdateRows {
			log.WarnContextf(ctx, "UpdateDocRows行数超过限制: 当前%d行, 最大允许%d行", updateRowsCount, maxUpdateRows)
			rsp.RetInfo = &pb.RetInfo{
				Code: pb.EnumErrorCode_INVALID_PARAM,
				Msg:  fmt.Sprintf("moox backend service: 单次set操作的行数超过限制，当前%d行，最大允许%d行", updateRowsCount, maxUpdateRows),
			}
			return nil, nil
		}
	}

	params, err := prepareSetFieldInfos(ctx, req)
	if err != nil {
		return nil, err
	}
	if params.RetInfo.Code != 0 {
		rsp.RetInfo = params.RetInfo
		return nil, nil
	}

	// 检查设备可用性
	if len(params.DeviceFieldMap) == 0 {
		log.ErrorContextf(ctx, "没有可用的存储设备配置")
		rsp.RetInfo = &pb.RetInfo{
			Code: pb.EnumErrorCode_NO_ROUTE_STORE_ITEM,
			Msg:  "没有配置存储设备",
		}
		return nil, nil
	}

	// 添加失败的行到响应中
	if len(params.FailedDocRows) > 0 {
		rsp.FailedRows = append(rsp.FailedRows, params.FailedDocRows...)
	}

	return params, nil
}

// buildSetTasks 构建设置任务
func (a *AdapterImpl) buildSetTasks(req *pb.SetFieldInfosReq,
	params *prepareSetFieldInfosParams) []setTask {
	var tasks []setTask
	tableID := utils.EscapeTableIDDash(req.GetTableId())
	for deviceID, fieldIDs := range params.DeviceFieldMap {
		// 为每个设备构建更新任务参数
		setParams := &dao.SetFieldParams{
			TableID:             tableID,
			DataType:            params.DataType,
			UpdateDocRows:       filterUpdateDocRowsByFields(req.GetUpdateDocRows(), fieldIDs),
			HistoricalRowsLimit: req.GetHistoricalRowsLimit(),
		}
		tasks = append(tasks, setTask{
			deviceID:     deviceID,
			TableID:      tableID,
			deviceParams: setParams,
		})
	}
	return tasks
}

// executeAndProcessTasks 执行任务并处理结果
func (a *AdapterImpl) executeAndProcessTasks(ctx context.Context, tasks []setTask, rsp *pb.SetFieldInfosRsp) error {
	// 执行更新任务并收集结果
	results, err := executeSetTasks(ctx, tasks)

	// 处理更新结果
	a.processTaskResults(ctx, results, rsp)

	if err != nil {
		log.ErrorContextf(ctx, "执行更新任务失败: %v", err)
		rsp.RetInfo = &pb.RetInfo{
			Code: pb.EnumErrorCode_INNER_ERR,
			Msg:  fmt.Sprintf("执行更新任务失败: %v", err),
		}
		return err
	}

	rsp.RetInfo = &pb.RetInfo{
		Code: pb.EnumErrorCode_SUCCESS,
		Msg:  "success",
	}
	return nil
}

// processTaskResults 处理任务结果
func (a *AdapterImpl) processTaskResults(ctx context.Context, results []*setResult, rsp *pb.SetFieldInfosRsp) {
	for _, result := range results {
		if result.err != nil {
			log.WarnContextf(ctx, "设备[%d]更新失败: %v", result.deviceID, result.err)
			continue
		}

		// 合并修改信息
		if len(result.modifyInfos) > 0 {
			rsp.ModifyInfos = append(rsp.ModifyInfos, result.modifyInfos...)
		}

		// 合并历史行数据
		if len(result.LastRows) > 0 {
			rsp.LastRows = append(rsp.LastRows, result.LastRows...)
		}

		// 合并失败行
		if len(result.failedRows) > 0 {
			rsp.FailedRows = append(rsp.FailedRows, result.failedRows...)
		}
	}
}
