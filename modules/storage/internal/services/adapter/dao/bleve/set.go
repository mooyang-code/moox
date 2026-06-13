package bleve

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/helper"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"

	"github.com/blevesearch/bleve/v2"
)

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// SetFieldInfos 统一更新数据接口(支持静态数据和时序数据的更新)
func (b *Bleve) SetFieldInfos(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
	log.DebugContextf(ctx, "+++++++ Bleve SetFieldInfos: %+v +++++++", params)

	// 添加调试信息：打印表ID和数据类型
	log.InfoContextf(ctx, "SetFieldInfos 调试: 表ID=%s, 数据类型=%v, 更新行数=%d",
		params.TableID, params.DataType, len(params.UpdateDocRows))

	// 获取或创建索引
	indexPath := b.getTableIndexPath(params.TableID)
	index, err := getIndex(ctx, indexPath)
	if err != nil {
		return nil, err
	}
	// 操作完成后关闭索引连接
	defer index.Close()

	// 初始化响应
	rsp := &pb.SetFieldInfosRsp{
		RetInfo: &pb.RetInfo{
			Code: 0,
			Msg:  "success",
		},
		ModifyInfos: []*pb.ModifyFieldInfo{},
		LastRows:    []*pb.DocRow{},
		FailedRows:  []*pb.FailedDocRow{},
	}
	if len(params.UpdateDocRows) == 0 {
		return rsp, nil
	}

	// 根据 data_type 参数优先判断数据类型
	switch params.DataType {
	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		// 直接处理为时序数据
		return b.processTimingDataUpdate(ctx, params.UpdateDocRows, params.HistoricalRowsLimit, index)
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		// 直接处理为静态数据
		return b.processStaticDataUpdate(ctx, params.UpdateDocRows, params.HistoricalRowsLimit, index)
	default:
		return rsp, nil
	}
}

// ============================================================================
// 业务逻辑层函数 - 处理具体业务逻辑
// ============================================================================

func (b *Bleve) processStaticDataUpdate(ctx context.Context,
	updateDocRows []*pb.UpdateDocRow, historicalRowsLimit uint32, index bleve.Index) (*pb.SetFieldInfosRsp, error) {
	// 初始化响应
	rsp := b.initializeStaticResponse()
	if len(updateDocRows) == 0 {
		return rsp, nil
	}

	batch := index.NewBatch()
	var successRows []*pb.DocRow
	var failedRows []*pb.FailedDocRow
	var modifyInfos []*pb.ModifyFieldInfo

	for _, updateRow := range updateDocRows {
		// 确保RowId存在（静态数据的行ID由上游access层确定，一般是对象ID）
		ensureRowID(updateRow)

		// 处理单行更新
		docRow, failedList, rowModifyInfos, err := b.processStaticRowUpdate(ctx, updateRow, index)
		if err != nil {
			failedRows = append(failedRows, b.handleRowProcessingError(ctx, updateRow, failedList, err))
			continue
		}

		// 添加到批处理
		if err := batch.Index(updateRow.RowId, docRow.Doc); err != nil {
			failedRows = append(failedRows, b.handleBatchIndexError(ctx, updateRow, failedList, err))
			continue
		}

		// 记录结果
		successRows = append(successRows, docRow.DocRow)
		modifyInfos = append(modifyInfos, rowModifyInfos...)

		// 如果有失败的字段，记录失败行
		if len(failedList) > 0 {
			failedRows = append(failedRows, &pb.FailedDocRow{
				RowId:      updateRow.RowId,
				FailedList: failedList,
			})
		}

		// 每100个文档执行一次批处理
		batch, err = b.executeBatchIfNeeded(ctx, batch, index)
		if err != nil {
			return nil, err
		}
	}

	// 完成批处理并准备响应
	finalSuccessRows, err := b.finalizeBatchAndResponse(ctx, batch, historicalRowsLimit, successRows, modifyInfos, failedRows, rsp, index)
	if err != nil {
		return nil, err
	}
	rsp.LastRows = finalSuccessRows
	return rsp, nil
}

// processTimingDataUpdate 时序数据：统一更新value接口
func (b *Bleve) processTimingDataUpdate(ctx context.Context,
	updateDocRows []*pb.UpdateDocRow, historicalRowsLimit uint32, index bleve.Index) (*pb.SetFieldInfosRsp, error) {
	// 初始化响应
	rsp := &pb.SetFieldInfosRsp{
		RetInfo: &pb.RetInfo{
			Code: 0,
			Msg:  "success",
		},
		ModifyInfos: []*pb.ModifyFieldInfo{},
		LastRows:    []*pb.DocRow{},
		FailedRows:  []*pb.FailedDocRow{},
	}
	if len(updateDocRows) == 0 {
		return rsp, nil
	}

	// 处理所有行的批量更新
	batch := index.NewBatch()
	successRows, failedRows, modifyInfos, err := b.processTimingRowsBatch(ctx, updateDocRows, batch, index)
	if err != nil {
		return nil, err
	}

	// 提交剩余的批处理
	if batch.Size() > 0 {
		if err := b.executeBatch(ctx, batch, index); err != nil {
			log.ErrorContextf(ctx, "Failed to execute final batch: %v", err)
			return nil, err
		}
	}

	// 获取历史行（如果需要）
	if historicalRowsLimit > 0 {
		historicalRows, err := b.getHistoricalTimingRows(ctx, historicalRowsLimit, index)
		if err == nil && len(historicalRows) > 0 {
			successRows = append(successRows, historicalRows...)
		}
	}
	rsp.ModifyInfos = modifyInfos
	rsp.LastRows = successRows
	rsp.FailedRows = failedRows
	return rsp, nil
}

// ============================================================================
// 辅助工具函数 - 底层工具和转换函数
// ============================================================================

// initializeStaticResponse 初始化静态数据更新响应
func (b *Bleve) initializeStaticResponse() *pb.SetFieldInfosRsp {
	return &pb.SetFieldInfosRsp{
		RetInfo: &pb.RetInfo{
			Code: 0,
			Msg:  "success",
		},
		ModifyInfos: []*pb.ModifyFieldInfo{},
		LastRows:    []*pb.DocRow{},
		FailedRows:  []*pb.FailedDocRow{},
	}
}

// handleRowProcessingError 处理行处理错误
func (b *Bleve) handleRowProcessingError(ctx context.Context, updateRow *pb.UpdateDocRow,
	failedList map[uint32]*pb.FailedInfo, err error) *pb.FailedDocRow {
	log.ErrorContextf(ctx, "Failed to process row update: %v", err)
	return &pb.FailedDocRow{
		RowId:      updateRow.RowId,
		FailedList: failedList,
	}
}

// handleBatchIndexError 处理批处理索引错误
func (b *Bleve) handleBatchIndexError(ctx context.Context, updateRow *pb.UpdateDocRow,
	failedList map[uint32]*pb.FailedInfo, err error) *pb.FailedDocRow {
	log.ErrorContextf(ctx, "Failed to add document to batch: %v", err)
	return &pb.FailedDocRow{
		RowId:      updateRow.RowId,
		FailedList: failedList,
	}
}

// executeBatchIfNeeded 根据需要执行批处理
func (b *Bleve) executeBatchIfNeeded(ctx context.Context, batch *bleve.Batch, index bleve.Index) (*bleve.Batch, error) {
	if batch.Size() >= 100 {
		if err := b.executeBatch(ctx, batch, index); err != nil {
			log.ErrorContextf(ctx, "Failed to execute batch: %v", err)
			return nil, err
		}
		return index.NewBatch(), nil
	}
	return batch, nil
}

// finalizeBatchAndResponse 完成批处理并准备响应
func (b *Bleve) finalizeBatchAndResponse(ctx context.Context, batch *bleve.Batch,
	historicalRowsLimit uint32, successRows []*pb.DocRow,
	modifyInfos []*pb.ModifyFieldInfo, failedRows []*pb.FailedDocRow,
	rsp *pb.SetFieldInfosRsp, index bleve.Index) ([]*pb.DocRow, error) {
	// 提交剩余的批处理
	if batch.Size() > 0 {
		if err := b.executeBatch(ctx, batch, index); err != nil {
			log.ErrorContextf(ctx, "Failed to execute final batch: %v", err)
			return nil, err
		}
	}

	// 获取历史行（如果需要）
	if historicalRowsLimit > 0 {
		historicalRows, err := b.getHistoricalRows(ctx, historicalRowsLimit, index)
		if err == nil && len(historicalRows) > 0 {
			successRows = append(successRows, historicalRows...)
		}
	}

	rsp.ModifyInfos = modifyInfos
	rsp.LastRows = successRows
	rsp.FailedRows = failedRows
	return successRows, nil
}

// 确保行ID存在
func ensureRowID(row *pb.UpdateDocRow) {
	if row.RowId == "" {
		row.RowId = helper.GenRowID()
	}
}

// 处理单行静态数据更新
type processedRow struct {
	DocRow *pb.DocRow     // 成功的文档行
	Doc    map[string]any // 存储的文档
}

// processStaticRowUpdate 处理单行静态数据更新
func (b *Bleve) processStaticRowUpdate(ctx context.Context, updateRow *pb.UpdateDocRow, index bleve.Index) (*processedRow, map[uint32]*pb.FailedInfo, []*pb.ModifyFieldInfo, error) {
	// 准备文档数据
	doc := make(map[string]any)
	doc["_row_id"] = updateRow.RowId
	doc["_ctime"] = utils.GetCurrentTimeStandard()
	doc["_mtime"] = utils.GetCurrentTimeStandard()

	// 获取当前文档内容
	docID := updateRow.RowId
	currentDoc, err := b.getCurrentDoc(docID, index)
	if err != nil {
		log.ErrorContextf(ctx, "Error retrieving current document: %v", err)
	}

	// 创建文档行以存储结果
	docRow := &pb.DocRow{
		RowId:  updateRow.RowId,
		Fields: make(map[uint32]*pb.FieldInfo),
	}

	// 失败字段记录
	failedList := make(map[uint32]*pb.FailedInfo)

	// 修改信息列表
	var modifyInfos []*pb.ModifyFieldInfo

	// 处理每个字段更新
	for fieldID, updateInfo := range updateRow.Fields {
		colName := b.fieldID2ColName(uint64(fieldID), "")

		// 获取当前字段值
		var currentFieldValue any
		if currentDoc != nil {
			currentFieldValue = currentDoc[colName]
		}

		// 处理字段更新并收集修改信息
		updateCtx := &FieldUpdateContext{
			Ctx:               ctx,
			Doc:               doc,
			DocRow:            docRow,
			FieldID:           fieldID,
			CurrentFieldValue: currentFieldValue,
			UpdateInfo:        updateInfo,
			FailedList:        failedList,
			IsTimingData:      false,
		}
		modifyInfo, err := b.processFieldUpdate(updateCtx)
		if err != nil {
			continue // 错误已添加到failedList
		}

		if modifyInfo != nil {
			modifyInfos = append(modifyInfos, modifyInfo)
		}
	}

	result := &processedRow{
		DocRow: docRow,
		Doc:    doc,
	}
	return result, failedList, modifyInfos, nil
}

// getCurrentDoc 获取当前文档
func (b *Bleve) getCurrentDoc(docID string, index bleve.Index) (map[string]any, error) {
	currentQuery := bleve.NewDocIDQuery([]string{docID})
	currentRequest := bleve.NewSearchRequest(currentQuery)
	currentRequest.Fields = []string{"*"}
	currentResults, err := index.Search(currentRequest)
	if err != nil {
		return nil, err
	}

	if len(currentResults.Hits) > 0 {
		return currentResults.Hits[0].Fields, nil
	}
	return nil, nil
}

// FieldUpdateContext 字段更新上下文参数结构体
type FieldUpdateContext struct {
	Ctx               context.Context           // 上下文
	Doc               map[string]any            // 文档数据
	DocRow            *pb.DocRow                // 文档行
	FieldID           uint32                    // 字段ID
	CurrentFieldValue any                       // 当前字段值
	UpdateInfo        *pb.UpdateFieldInfo       // 更新信息
	FailedList        map[uint32]*pb.FailedInfo // 失败字段列表
	IsTimingData      bool                      // 是否为时序数据
}

// processFieldUpdate 处理字段更新（使用上下文结构体参数）
func (b *Bleve) processFieldUpdate(ctx *FieldUpdateContext) (*pb.ModifyFieldInfo, error) {
	colName := b.fieldID2ColName(uint64(ctx.FieldID), "")
	fieldInfo := cache.GetFieldInfoByID(int(ctx.FieldID))

	// 记录调试信息
	b.logFieldUpdateDebug(ctx, colName, fieldInfo)

	// 执行字段更新操作
	if err := b.executeFieldUpdate(ctx, colName, fieldInfo); err != nil {
		return nil, err
	}

	// 创建并返回修改信息
	return b.createModifyInfo(ctx), nil
}

// logFieldUpdateDebug 记录字段更新调试信息
func (b *Bleve) logFieldUpdateDebug(ctx *FieldUpdateContext, colName string, fieldInfo *cache.Field) {
	log.InfoContextf(ctx.Ctx, "字段写入调试: 字段ID=%d, 列名=%s, 更新类型=%v",
		ctx.FieldID, colName, ctx.UpdateInfo.UpdateType)

	if fieldInfo != nil {
		log.InfoContextf(ctx.Ctx, "  字段缓存信息: 接口名=%s, 类型=%d, 项目ID=%d",
			fieldInfo.InterfaceName, fieldInfo.FieldPrimaryFormat, fieldInfo.ProjID)
	} else {
		log.WarnContextf(ctx.Ctx, "  字段缓存中未找到字段ID=%d的信息", ctx.FieldID)
	}
}

// executeFieldUpdate 执行字段更新操作
func (b *Bleve) executeFieldUpdate(ctx *FieldUpdateContext, colName string, fieldInfo *cache.Field) error {
	switch ctx.UpdateInfo.UpdateType {
	case pb.EnumUpdateType_SET_UPDATE:
		return b.handleSetUpdate(ctx, colName, fieldInfo)
	case pb.EnumUpdateType_DEL_UPDATE:
		return b.handleDelUpdate(ctx, colName)
	case pb.EnumUpdateType_APPEND_UPDATE:
		return b.handleAppendUpdate(ctx, colName)
	default:
		return b.addFailedInfo(ctx.FieldID, ctx.FailedList, pb.EnumErrorCode_INVALID_OP_TYPE,
			fmt.Sprintf("Unsupported operation type: %v", ctx.UpdateInfo.UpdateType))
	}
}

// handleSetUpdate 处理SET更新操作
func (b *Bleve) handleSetUpdate(ctx *FieldUpdateContext, colName string, fieldInfo *cache.Field) error {
	fieldValue, err := b.convertField(ctx.UpdateInfo.FieldInfo)
	if err != nil {
		return b.addFailedInfo(ctx.FieldID, ctx.FailedList, pb.EnumErrorCode_INVALID_OP_TYPE,
			fmt.Sprintf("Invalid field value: %v", err))
	}

	// 按字段缓存的期望类型强制转换
	if fieldInfo != nil {
		fieldValue = convertFieldByExpectedType(fieldValue, pb.EnumFieldType(fieldInfo.FieldPrimaryFormat))
	}

	ctx.Doc[colName] = fieldValue
	ctx.DocRow.Fields[ctx.FieldID] = ctx.UpdateInfo.FieldInfo

	log.InfoContextf(ctx.Ctx, "  字段值写入: %s = %v (类型: %T)", colName, fieldValue, fieldValue)
	return nil
}

// handleDelUpdate 处理DELETE更新操作
func (b *Bleve) handleDelUpdate(ctx *FieldUpdateContext, colName string) error {
	delete(ctx.Doc, colName)
	return nil
}

// handleAppendUpdate 处理APPEND更新操作
func (b *Bleve) handleAppendUpdate(ctx *FieldUpdateContext, colName string) error {
	newValue, err := b.appendToField(ctx.CurrentFieldValue, ctx.UpdateInfo.FieldInfo)
	if err != nil {
		return b.addFailedInfo(ctx.FieldID, ctx.FailedList, pb.EnumErrorCode_INVALID_OP_TYPE,
			fmt.Sprintf("Invalid append operation: %v", err))
	}

	ctx.Doc[colName] = newValue
	ctx.DocRow.Fields[ctx.FieldID] = ctx.UpdateInfo.FieldInfo
	return nil
}

// addFailedInfo 添加失败信息的辅助函数
func (b *Bleve) addFailedInfo(fieldID uint32, failedList map[uint32]*pb.FailedInfo,
	code pb.EnumErrorCode, msg string) error {
	failedList[fieldID] = &pb.FailedInfo{
		Code: code,
		Msg:  msg,
	}
	return fmt.Errorf("%s", msg)
}

// createModifyInfo 创建修改信息
func (b *Bleve) createModifyInfo(ctx *FieldUpdateContext) *pb.ModifyFieldInfo {
	oldDocRow := &pb.DocRow{
		RowId:  ctx.DocRow.RowId,
		Fields: make(map[uint32]*pb.FieldInfo),
	}
	newDocRow := &pb.DocRow{
		RowId:  ctx.DocRow.RowId,
		Fields: make(map[uint32]*pb.FieldInfo),
	}

	// 如果是时序数据，设置 Times 字段
	if ctx.IsTimingData {
		oldDocRow.Times = ctx.DocRow.Times
		newDocRow.Times = ctx.DocRow.Times
	}

	// 记录旧值
	if ctx.CurrentFieldValue != nil {
		oldDocRow.Fields[ctx.FieldID] = &pb.FieldInfo{
			FieldId:   ctx.FieldID,
			FieldType: ctx.UpdateInfo.FieldInfo.FieldType,
		}
	}

	// 记录新值
	if ctx.UpdateInfo.FieldInfo != nil {
		newDocRow.Fields[ctx.FieldID] = ctx.UpdateInfo.FieldInfo
	}

	return &pb.ModifyFieldInfo{
		OldDocRow: oldDocRow,
		NewDocRow: newDocRow,
	}
}

// processTimingRowsBatch 批量处理时序数据行更新
func (b *Bleve) processTimingRowsBatch(ctx context.Context, updateDocRows []*pb.UpdateDocRow,
	batch *bleve.Batch, index bleve.Index) ([]*pb.DocRow, []*pb.FailedDocRow, []*pb.ModifyFieldInfo, error) {
	var successRows []*pb.DocRow
	var failedRows []*pb.FailedDocRow
	var modifyInfos []*pb.ModifyFieldInfo

	for _, updateRow := range updateDocRows {
		// 确保RowId存在及时间戳有效
		ensureRowIDAndTimestamp(updateRow)

		// 处理单行更新
		docRow, failedList, rowModifyInfos, err := b.processTimingRowUpdate(ctx, updateRow, index)
		if err != nil {
			log.ErrorContextf(ctx, "Failed to process row update: %v", err)
			failedRows = append(failedRows, &pb.FailedDocRow{
				RowId:      updateRow.RowId,
				Times:      updateRow.Times,
				FailedList: failedList,
			})
			continue
		}

		// 使用计算后的docID作为Bleve的文档ID，确保时序数据按时间戳覆盖
		docID := docRow.DocRow.RowId

		// 添加到批处理
		if err := batch.Index(docID, docRow.Doc); err != nil {
			log.ErrorContextf(ctx, "Failed to add document to batch: %v", err)
			failedRows = append(failedRows, &pb.FailedDocRow{
				RowId:      updateRow.RowId,
				Times:      updateRow.Times,
				FailedList: failedList,
			})
			continue
		}

		// 记录结果
		successRows = append(successRows, docRow.DocRow)
		modifyInfos = append(modifyInfos, rowModifyInfos...)

		// 如果有失败的字段，记录失败行
		if len(failedList) > 0 {
			failedRows = append(failedRows, &pb.FailedDocRow{
				RowId:      updateRow.RowId,
				Times:      updateRow.Times,
				FailedList: failedList,
			})
		}

		// 每100个文档执行一次批处理
		if batch.Size() >= 100 {
			if err := b.executeBatch(ctx, batch, index); err != nil {
				log.ErrorContextf(ctx, "Failed to execute batch: %v", err)
				return nil, nil, nil, err
			}
		}
	}
	return successRows, failedRows, modifyInfos, nil
}

// ensureRowIDAndTimestamp 确保行ID存在且时间戳有效
func ensureRowIDAndTimestamp(row *pb.UpdateDocRow) {
	// 确保RowId存在
	if row.RowId == "" {
		row.RowId = helper.GenRowID()
	}

	// 确保时间戳存在
	if row.Times == "" {
		row.Times = utils.GetCurrentTimeStandard()
	}
}

// processTimingRowUpdate 处理单行时序数据更新
func (b *Bleve) processTimingRowUpdate(ctx context.Context,
	updateRow *pb.UpdateDocRow, index bleve.Index) (*processedRow, map[uint32]*pb.FailedInfo, []*pb.ModifyFieldInfo, error) {
	// 准备文档数据并设置系统字段
	doc, docID := b.prepareTimingDocumentData(updateRow)

	// 获取当前文档内容
	currentDoc, err := b.getCurrentDoc(docID, index)
	if err != nil {
		log.ErrorContextf(ctx, "Error retrieving current document: %v", err)
	}

	// 创建文档行以存储结果
	docRow := &pb.DocRow{
		RowId:  docID, // 使用转换后的时间戳字符串作为行ID
		Times:  updateRow.Times,
		Fields: make(map[uint32]*pb.FieldInfo),
	}

	failedList := make(map[uint32]*pb.FailedInfo)
	var modifyInfos []*pb.ModifyFieldInfo

	// 处理每个字段更新
	for fieldID, updateInfo := range updateRow.Fields {
		colName := b.fieldID2ColName(uint64(fieldID), "")

		// 获取当前字段值
		var currentFieldValue any
		if currentDoc != nil {
			currentFieldValue = currentDoc[colName]
		}

		// 处理字段更新并收集修改信息
		updateCtx := &FieldUpdateContext{
			Ctx:               ctx,
			Doc:               doc,
			DocRow:            docRow,
			FieldID:           fieldID,
			CurrentFieldValue: currentFieldValue,
			UpdateInfo:        updateInfo,
			FailedList:        failedList,
			IsTimingData:      true,
		}
		modifyInfo, err := b.processFieldUpdate(updateCtx)
		if err != nil {
			continue // 错误已添加到failedList
		}
		if modifyInfo != nil {
			modifyInfos = append(modifyInfos, modifyInfo)
		}
	}

	result := &processedRow{
		DocRow: docRow,
		Doc:    doc,
	}
	return result, failedList, modifyInfos, nil
}

// convertField 将FieldInfo转换为适合存储的接口类型
func (b *Bleve) convertField(fieldInfo *pb.FieldInfo) (any, error) {
	if fieldInfo == nil {
		return nil, fmt.Errorf("fieldInfo is nil")
	}

	switch fieldInfo.FieldType {
	case pb.EnumFieldType_STR_FIELD:
		return fieldInfo.GetSimpleValue().GetStr(), nil
	case pb.EnumFieldType_INT_FIELD:
		return fieldInfo.GetSimpleValue().GetInt(), nil
	case pb.EnumFieldType_FLOAT_FIELD:
		return fieldInfo.GetSimpleValue().GetFloat(), nil
	case pb.EnumFieldType_TIME_FIELD:
		return fieldInfo.GetSimpleValue().GetTime(), nil
	case pb.EnumFieldType_MAP_KV_FIELD:
		// 将map转换为json
		mapValues := make(map[string]any)
		if mapValue := fieldInfo.GetMapValue(); mapValue != nil {
			for k, entry := range mapValue.GetEntries() {
				switch entry.Type {
				case pb.EnumFieldType_STR_FIELD:
					mapValues[k] = entry.GetValue().GetStr()
				case pb.EnumFieldType_INT_FIELD:
					mapValues[k] = entry.GetValue().GetInt()
				case pb.EnumFieldType_FLOAT_FIELD:
					mapValues[k] = entry.GetValue().GetFloat()
				case pb.EnumFieldType_TIME_FIELD:
					mapValues[k] = entry.GetValue().GetTime()
				default:
					mapValues[k] = fmt.Sprintf("%v", entry)
				}
			}
		}
		return mapValues, nil
	default:
		return fmt.Sprintf("%v", fieldInfo), nil
	}
}

// convertFieldByExpectedType 根据字段缓存的期望类型，将字符串数字纠正为数值类型
func convertFieldByExpectedType(val any, expected pb.EnumFieldType) any {
	switch expected {
	case pb.EnumFieldType_INT_FIELD:
		switch v := val.(type) {
		case string:
			if iv, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return iv
			}
		case float64:
			return int64(v)
		}
	case pb.EnumFieldType_FLOAT_FIELD:
		switch v := val.(type) {
		case string:
			if fv, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return fv
			}
		case int64:
			return float64(v)
		case int:
			return float64(v)
		}
	case pb.EnumFieldType_TIME_FIELD:
		// 统一保持 "YYYY-MM-DD HH:MM:SS" 格式
		switch v := val.(type) {
		case string:
			s := strings.TrimSpace(v)
			// 如果已经是目标格式，直接返回
			if _, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
				return s
			}
			// 尝试解析 RFC3339 格式，转换为目标格式
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t.UTC().Format("2006-01-02 15:04:05")
			}
			// 尝试解析其他常见时间格式
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC); err == nil {
				return t.UTC().Format("2006-01-02 15:04:05")
			}
			// 兜底直接返回原字符串
			return s
		}
	}
	return val
}

// appendIntVecField 处理整型向量字段的追加操作
func (b *Bleve) appendIntVecField(currentValue any, newFieldInfo *pb.FieldInfo) (any, error) {
	var currentIntList []int64

	// 尝试将当前值转换为[]int64
	switch v := currentValue.(type) {
	case []int64:
		currentIntList = v
	case []any:
		// 尝试将[]interface{}转换为[]int64
		for _, item := range v {
			if intVal, ok := item.(int64); ok {
				currentIntList = append(currentIntList, intVal)
			} else if floatVal, ok := item.(float64); ok {
				// JSON反序列化可能会将整数解析为float64
				currentIntList = append(currentIntList, int64(floatVal))
			} else {
				return nil, fmt.Errorf("item in array is not an integer: %v", item)
			}
		}
	default:
		return nil, fmt.Errorf("current value is not an integer array: %T", currentValue)
	}

	// 获取新的整型列表
	newIntList := newFieldInfo.GetSimpleValue().GetIntList()
	if newIntList == nil {
		return nil, fmt.Errorf("new value does not contain a valid int list")
	}

	// 合并列表
	result := make([]int64, len(currentIntList)+len(newIntList.GetValues()))
	copy(result, currentIntList)
	copy(result[len(currentIntList):], newIntList.GetValues())
	return result, nil
}

// appendSetField 处理字符串集合字段的追加操作
func (b *Bleve) appendSetField(currentValue any, newFieldInfo *pb.FieldInfo) (any, error) {
	var currentStrList []string

	// 尝试将当前值转换为[]string
	switch v := currentValue.(type) {
	case []string:
		currentStrList = v
	case []any:
		// 尝试将[]interface{}转换为[]string
		for _, item := range v {
			if strVal, ok := item.(string); ok {
				currentStrList = append(currentStrList, strVal)
			} else {
				return nil, fmt.Errorf("item in array is not a string: %v", item)
			}
		}
	default:
		return nil, fmt.Errorf("current value is not a string array: %T", currentValue)
	}

	// 获取新的字符串列表
	newStrList := newFieldInfo.GetSimpleValue().GetStrList()
	if newStrList == nil {
		return nil, fmt.Errorf("new value does not contain a valid string list")
	}

	// 合并列表
	result := make([]string, len(currentStrList)+len(newStrList.GetValues()))
	copy(result, currentStrList)
	copy(result[len(currentStrList):], newStrList.GetValues())
	return result, nil
}

// appendMapField 处理映射字段的追加操作
func (b *Bleve) appendMapField(currentValue any, newFieldInfo *pb.FieldInfo) (any, error) {
	currentMap, ok := currentValue.(map[string]any)
	if !ok {
		// 尝试进行类型转换
		jsonBytes, err := json.Marshal(currentValue)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal current value: %v", err)
		}
		var tempMap map[string]any
		if err := json.Unmarshal(jsonBytes, &tempMap); err != nil {
			return nil, fmt.Errorf("current value cannot be converted to map: %v", err)
		}
		currentMap = tempMap
	}

	// 转换新的map值
	newValues, err := b.convertField(newFieldInfo)
	if err != nil {
		return nil, err
	}
	newMap, ok := newValues.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("new value is not a map")
	}

	// 合并maps
	result := make(map[string]any)
	maps.Copy(result, currentMap)
	maps.Copy(result, newMap)
	return result, nil
}

// appendToField 将值追加到字段
func (b *Bleve) appendToField(currentValue any, newFieldInfo *pb.FieldInfo) (any, error) {
	// 根据字段类型和当前值执行不同的追加操作
	if currentValue == nil {
		// 如果当前值为空，直接返回新值
		return b.convertField(newFieldInfo)
	}

	switch newFieldInfo.FieldType {
	case pb.EnumFieldType_STR_FIELD:
		// 对于字符串，我们可以连接它们
		currentStr, ok := currentValue.(string)
		if !ok {
			return nil, fmt.Errorf("current value is not a string")
		}
		return currentStr + newFieldInfo.GetSimpleValue().GetStr(), nil

	case pb.EnumFieldType_INT_VEC_FIELD:
		return b.appendIntVecField(currentValue, newFieldInfo)

	case pb.EnumFieldType_SET_FIELD:
		return b.appendSetField(currentValue, newFieldInfo)

	case pb.EnumFieldType_MAP_KV_FIELD:
		return b.appendMapField(currentValue, newFieldInfo)

	default:
		// 对于其他类型，不支持追加
		return nil, fmt.Errorf("append not supported for field type: %v", newFieldInfo.FieldType)
	}
}

// getHistoricalRows 获取静态历史行
func (b *Bleve) getHistoricalRows(ctx context.Context, limit uint32, index bleve.Index) ([]*pb.DocRow, error) {
	// 获取所有文档
	query := bleve.NewMatchAllQuery()
	searchRequest := bleve.NewSearchRequest(query)
	searchRequest.Size = int(limit)
	searchRequest.Fields = []string{"*"}
	searchRequest.SortBy([]string{"-_ctime"}) // 按创建时间降序排序

	// 执行搜索
	searchResults, err := index.Search(searchRequest)
	if err != nil {
		log.ErrorContextf(ctx, "Bleve search error: %v", err)
		return nil, err
	}

	// 处理搜索结果
	var results []*pb.DocRow
	for _, hit := range searchResults.Hits {
		docRow := b.documentToDocRow(hit.Fields)
		results = append(results, docRow)
	}
	return results, nil
}

// getHistoricalTimingRows 获取时序历史行
func (b *Bleve) getHistoricalTimingRows(ctx context.Context, limit uint32, index bleve.Index) ([]*pb.DocRow, error) {
	// 获取所有文档
	query := bleve.NewMatchAllQuery()
	searchRequest := bleve.NewSearchRequest(query)
	searchRequest.Size = int(limit)
	searchRequest.Fields = []string{"*"}
	searchRequest.SortBy([]string{"-_times"}) // 按时间戳降序排序

	// 执行搜索
	searchResults, err := index.Search(searchRequest)
	if err != nil {
		log.ErrorContextf(ctx, "Bleve search error: %v", err)
		return nil, err
	}

	// 处理搜索结果
	var results []*pb.DocRow
	for _, hit := range searchResults.Hits {
		docRow := b.documentToDocRow(hit.Fields)
		results = append(results, docRow)
	}
	return results, nil
}

// generateRowIDFromTime 从时间字符串生成行ID
// 尝试解析时间并转换为时间戳字符串，如果失败则使用原RowId
func (b *Bleve) generateRowIDFromTime(timeStr string) string {
	if timeStr == "" {
		return ""
	}

	// 使用utils.NormalizeTimeString统一时间格式，然后解析
	normalizedTime := utils.NormalizeTimeString(timeStr)
	if t, err := time.Parse("2006-01-02 15:04:05", normalizedTime); err == nil {
		return fmt.Sprintf("%d", t.Unix())
	}
	return ""
}

// prepareTimingDocumentData 准备时序数据的文档数据和系统字段
// 返回准备好的文档数据和文档ID
func (b *Bleve) prepareTimingDocumentData(updateRow *pb.UpdateDocRow) (map[string]any, string) {
	doc := make(map[string]any)

	// 设置系统字段
	doc["_times"] = utils.NormalizeTimeString(updateRow.Times)
	doc["_ctime"] = utils.GetCurrentTimeStandard()
	doc["_mtime"] = utils.GetCurrentTimeStandard()

	// 生成行ID并设置到文档中
	rowID := b.generateRowIDFromTime(updateRow.Times)
	if rowID == "" {
		rowID = updateRow.RowId
	}
	doc["_row_id"] = rowID
	return doc, rowID
}
