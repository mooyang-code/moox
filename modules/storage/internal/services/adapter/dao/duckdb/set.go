package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/go-commlib/tinyfunc"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/helper"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-database/localcache"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

// docInfo 结构用于批处理行信息
type docInfo struct {
	updateRow    *pb.UpdateDocRow
	fieldIDs     []uint32
	docRow       *pb.DocRow                // 用于存储结果
	needInsert   bool                      // 是否需要插入新行
	setParts     []string                  // 用于UPDATE语句
	args         []any                     // UPDATE语句的参数
	insertFields map[uint32]*pb.FieldInfo  // 用于INSERT语句
	failedList   map[uint32]*pb.FailedInfo // 失败的字段信息
}

// TableCacheValue 表缓存值
type TableCacheValue struct {
	TableID    string    `json:"table_id"`
	CreateTime time.Time `json:"create_time"`
	Status     string    `json:"status"` // "created", "exists", "failed"
}

type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// SetFieldInfos 统一更新数据接口(支持静态数据和时序数据的更新)
func (d *DuckDB) SetFieldInfos(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
	log.DebugContextf(ctx, "+++++++ DuckDB SetFieldInfos: %+v +++++++", params)
	// 确保表存在
	if err := d.ensureTableExists(ctx, params.TableID, params.DataType); err != nil {
		log.ErrorContextf(ctx, "Failed to ensure table exists: %v", err)
		return nil, err
	}
	tableID := utils.EscapeTableIDDash(params.TableID)
	d.tableID = tableID

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
		timingRsp, err := d.processTimingDataUpdate(ctx, tableID, params.UpdateDocRows, params.HistoricalRowsLimit)
		if err != nil {
			log.ErrorContextf(ctx, "Failed to process timing data update: %v", err)
		}
		if timingRsp != nil {
			rsp.ModifyInfos = append(rsp.ModifyInfos, timingRsp.ModifyInfos...)
			rsp.LastRows = append(rsp.LastRows, timingRsp.LastRows...)
			rsp.FailedRows = append(rsp.FailedRows, timingRsp.FailedRows...)
		}
		return rsp, nil
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		// 直接处理为静态数据
		staticRsp, err := d.processStaticDataUpdate(ctx, tableID, params.UpdateDocRows, params.HistoricalRowsLimit)
		if err != nil {
			log.ErrorContextf(ctx, "Failed to process static data update: %v", err)
		}
		if staticRsp != nil {
			rsp.ModifyInfos = append(rsp.ModifyInfos, staticRsp.ModifyInfos...)
			rsp.LastRows = append(rsp.LastRows, staticRsp.LastRows...)
			rsp.FailedRows = append(rsp.FailedRows, staticRsp.FailedRows...)
		}
		return rsp, nil
	}
	return rsp, nil
}

// prepareRowsForUpdate 准备行用于插入或更新操作
func (d *DuckDB) prepareRowsForUpdate(ctx context.Context,
	rowMap map[string]*docInfo,
	currentValuesMap map[string]map[uint32]*pb.FieldInfo,
) ([]*docInfo, []*docInfo, []*pb.FailedDocRow, map[uint32]*pb.ModifyFieldInfo) {
	var insertRows []*docInfo
	var updateRows []*docInfo
	var failedDocRows []*pb.FailedDocRow
	allInfos := make(map[uint32]*pb.ModifyFieldInfo) // 收集所有修改信息

	// 处理每一行
	for rowID, info := range rowMap {
		currentValues := currentValuesMap[rowID]
		info.needInsert = len(currentValues) == 0

		// 处理字段更新并收集修改信息
		rowInfos := d.processRowFields(ctx, info, currentValues)

		// 合并修改信息
		for fieldID, modifyInfo := range rowInfos {
			allInfos[fieldID] = modifyInfo
		}

		// 分类处理结果
		if info.needInsert {
			if len(info.insertFields) > 0 {
				insertRows = append(insertRows, info)
			}
		} else {
			if len(info.setParts) > 0 {
				updateRows = append(updateRows, info)
			}
		}

		// 处理失败的字段
		if len(info.failedList) > 0 {
			failedDocRow := &pb.FailedDocRow{
				Times:      info.updateRow.Times,
				RowId:      info.updateRow.RowId,
				FailedList: info.failedList,
			}
			failedDocRows = append(failedDocRows, failedDocRow)
		}
	}
	return insertRows, updateRows, failedDocRows, allInfos
}

// processRowFields 处理行字段更新
func (d *DuckDB) processRowFields(ctx context.Context,
	info *docInfo, currentValues map[uint32]*pb.FieldInfo) map[uint32]*pb.ModifyFieldInfo {
	infos := make(map[uint32]*pb.ModifyFieldInfo)
	for fieldID, updateInfo := range info.updateRow.Fields {
		// 获取字段类型
		fieldType, err := d.getFieldType(ctx, fieldID)
		if err != nil {
			info.failedList[fieldID] = &pb.FailedInfo{
				Code: pb.EnumErrorCode_FIELD_INFO_NOT_EXIST,
				Msg:  fmt.Sprintf("Field info not exist: %v", err),
			}
			continue
		}

		// 获取当前值
		var currentFieldValue *pb.FieldInfo
		if val, exists := currentValues[fieldID]; exists {
			currentFieldValue = val
		}

		// 处理更新操作
		newValue, modifyInfo, err := d.processFieldUpdate(updateInfo, currentFieldValue, fieldType)
		if err != nil {
			info.failedList[fieldID] = &pb.FailedInfo{
				Code: pb.EnumErrorCode_INVALID_OP_TYPE,
				Msg:  fmt.Sprintf("Invalid operation: %v", err),
			}
			continue
		}

		// 保存修改信息
		if modifyInfo != nil {
			infos[fieldID] = modifyInfo
		}

		// 准备字段操作
		if info.needInsert {
			info.insertFields[fieldID] = updateInfo.FieldInfo
		} else {
			columnName := d.formatColumnName(ctx, d.tableID, fieldID)
			if columnName == "" {
				log.DebugContextf(ctx, "#Skip field %d: formatColumnName returned empty string (不会导致插入失败，只会忽略该字段)", fieldID)
				info.failedList[fieldID] = &pb.FailedInfo{
					Code: pb.EnumErrorCode_FIELD_INFO_NOT_EXIST,
					Msg:  "Failed to get column name for field",
				}
				continue
			}
			info.setParts = append(info.setParts, fmt.Sprintf("%s = ?", columnName))
			info.args = append(info.args, newValue)
		}

		// 添加到结果
		if updateInfo.FieldInfo != nil {
			info.docRow.Fields[fieldID] = updateInfo.FieldInfo
		}
	}
	return infos
}

// ============================================================================
// 业务逻辑层函数 - 处理具体业务逻辑
// ============================================================================

func (d *DuckDB) processStaticDataUpdate(ctx context.Context, tableID string,
	updateDocRows []*pb.UpdateDocRow, historicalRowsLimit uint32) (*pb.SetFieldInfosRsp, error) {
	log.DebugContextf(ctx, "processStaticDataUpdate tableID: %s, updateDocRows: %+v", tableID, updateDocRows)
	// 初始化响应
	rsp := d.initializeResponse()
	if len(updateDocRows) == 0 {
		return rsp, nil
	}

	// 预处理行数据
	_, rowMap := d.preprocessRows(ctx, updateDocRows)

	// 静态数据需要获取当前值用于生成变更通知（包含新旧值对比）
	var rowIDs []string
	for rowID := range rowMap {
		rowIDs = append(rowIDs, rowID)
	}
	currentValuesMap, err := d.batchGetStaticDocRows(ctx, tableID, rowIDs, rowMap)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to batch get current field values: %v", err)
		return rsp, err
	}

	// 准备静态数据 upsert 操作
	allRows, failedRows, modInfos := d.prepareStaticRowsForUpsert(rowMap, currentValuesMap)
	rsp.FailedRows = append(rsp.FailedRows, failedRows...)

	// 获取历史数据
	historicalRows, err := d.fetchHistoricalData(ctx, tableID, historicalRowsLimit)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to fetch historical data: %v", err)
		// 历史数据获取失败不影响主要操作，继续执行
	} else {
		rsp.LastRows = append(rsp.LastRows, historicalRows...)
	}

	// 执行统一的 INSERT ... ON CONFLICT DO UPDATE 操作
	upsertFailedRows := d.executeStaticUpsertOperations(ctx, tableID, allRows)
	rsp.FailedRows = append(rsp.FailedRows, upsertFailedRows...)

	// 添加修改信息
	modifyInfos := d.convertModifyInfosFromMap(modInfos)
	rsp.ModifyInfos = append(rsp.ModifyInfos, modifyInfos...)
	return rsp, nil
}

// processTimingDataUpdate 时序数据：统一更新value接口
func (d *DuckDB) processTimingDataUpdate(ctx context.Context, tableID string,
	updateDocRows []*pb.UpdateDocRow, historicalRowsLimit uint32) (*pb.SetFieldInfosRsp, error) {
	// 初始化响应
	rsp := d.initializeResponse()
	if len(updateDocRows) == 0 {
		return rsp, nil
	}

	// 预处理时序数据
	timeMap := d.preprocessTimingRows(ctx, updateDocRows)

	// 时序数据不获取当前值，因为过去的数据不会修改
	// 直接准备所有行数据用于 INSERT ... ON DUPLICATE KEY UPDATE
	allRows, failedRows, infos := d.prepareTimingRowsForUpsert(ctx, timeMap)
	rsp.FailedRows = append(rsp.FailedRows, failedRows...)

	// 获取历史数据
	historicalRows, err := d.fetchHistoricalData(ctx, tableID, historicalRowsLimit)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to fetch historical data: %v", err)
		// 历史数据获取失败不影响主要操作，继续执行
	} else {
		rsp.LastRows = append(rsp.LastRows, historicalRows...)
	}

	// 执行统一的 INSERT ... ON DUPLICATE KEY UPDATE 操作
	upsertFailedRows := d.executeTimingUpsertOperations(ctx, tableID, allRows)
	rsp.FailedRows = append(rsp.FailedRows, upsertFailedRows...)

	// 添加修改信息
	modifyInfos := d.convertModifyInfosFromMap(infos)
	rsp.ModifyInfos = append(rsp.ModifyInfos, modifyInfos...)
	return rsp, nil
}

// ============================================================================
// 辅助工具函数 - 底层工具和转换函数
// ============================================================================

// initializeResponse 初始化响应
func (d *DuckDB) initializeResponse() *pb.SetFieldInfosRsp {
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

// preprocessRows 预处理行数据
func (d *DuckDB) preprocessRows(ctx context.Context, updateDocRows []*pb.UpdateDocRow) ([]string, map[string]*docInfo) {
	var rowIDs []string
	rowMap := make(map[string]*docInfo)

	for _, updateRow := range updateDocRows {
		// 检查RowId是否为空，如果为空则生成唯一ID
		if updateRow.RowId == "" {
			updateRow.RowId = helper.GenRowID()
			log.InfoContextf(ctx, "Generated unique ID for row: %s", updateRow.RowId)
		}

		// 收集行信息
		rowMap[updateRow.RowId] = &docInfo{
			updateRow: updateRow,
			fieldIDs:  tinyfunc.Keys(updateRow.Fields),
			docRow: &pb.DocRow{
				RowId:  updateRow.RowId,
				Fields: make(map[uint32]*pb.FieldInfo),
			},
			setParts:     []string{},
			args:         []any{},
			insertFields: make(map[uint32]*pb.FieldInfo),
			failedList:   make(map[uint32]*pb.FailedInfo),
		}
		rowIDs = append(rowIDs, updateRow.RowId)
	}
	return rowIDs, rowMap
}

// prepareStaticRowsForUpsert 为静态数据准备 upsert 操作
func (d *DuckDB) prepareStaticRowsForUpsert(rowMap map[string]*docInfo,
	currentValuesMap map[string]map[uint32]*pb.FieldInfo) ([]*docInfo, []*pb.FailedDocRow, map[uint32]*pb.ModifyFieldInfo) {
	var allRows []*docInfo
	var failedDocRows []*pb.FailedDocRow
	allInfos := make(map[uint32]*pb.ModifyFieldInfo) // 收集所有修改信息

	// 处理每一行
	for rowID, info := range rowMap {
		// 获取当前行的值（用于生成变更通知）
		currentFieldsMap := currentValuesMap[rowID]
		var currentRow *pb.DocRow
		if currentFieldsMap != nil {
			currentRow = &pb.DocRow{
				RowId:  rowID,
				Fields: currentFieldsMap,
			}
		}

		// 处理字段更新并收集修改信息（静态数据需要当前值用于新旧值对比）
		rowInfos := d.processStaticRowFields(info, currentRow)

		// 合并修改信息
		for fieldID, modifyInfo := range rowInfos {
			allInfos[fieldID] = modifyInfo
		}

		// 如果有有效字段，添加到处理列表
		if len(info.insertFields) > 0 {
			allRows = append(allRows, info)
		}

		// 处理失败的字段
		if len(info.failedList) > 0 {
			failedDocRow := &pb.FailedDocRow{
				RowId:      info.updateRow.RowId,
				FailedList: info.failedList,
			}
			failedDocRows = append(failedDocRows, failedDocRow)
		}
	}
	return allRows, failedDocRows, allInfos
}

// processStaticRowFields 处理静态数据行字段更新（需要当前值用于新旧值对比）
func (d *DuckDB) processStaticRowFields(info *docInfo, currentRow *pb.DocRow) map[uint32]*pb.ModifyFieldInfo {
	infos := make(map[uint32]*pb.ModifyFieldInfo) // 局部变量，避免并发冲突
	for fieldID, updateInfo := range info.updateRow.Fields {
		// 获取当前字段值（用于生成变更通知）
		var currentFieldValue *pb.FieldInfo
		if currentRow != nil && currentRow.Fields != nil {
			currentFieldValue = currentRow.Fields[fieldID]
		}

		// 生成修改信息（静态数据的变更通知包含新旧值对比）
		modifyInfo := &pb.ModifyFieldInfo{
			NewDocRow: &pb.DocRow{
				RowId: info.updateRow.RowId,
				Fields: map[uint32]*pb.FieldInfo{
					fieldID: updateInfo.FieldInfo,
				},
			},
		}

		// 如果有当前值，添加到旧值中
		if currentFieldValue != nil {
			modifyInfo.OldDocRow = &pb.DocRow{
				RowId: info.updateRow.RowId,
				Fields: map[uint32]*pb.FieldInfo{
					fieldID: currentFieldValue,
				},
			}
		}
		infos[fieldID] = modifyInfo

		// 准备字段数据用于插入
		info.insertFields[fieldID] = updateInfo.FieldInfo

		// 添加到结果
		if updateInfo.FieldInfo != nil {
			info.docRow.Fields[fieldID] = updateInfo.FieldInfo
		}
	}
	return infos
}

// executeStaticUpsertOperations 执行静态数据的 upsert 操作
func (d *DuckDB) executeStaticUpsertOperations(ctx context.Context, tableID string,
	allRows []*docInfo) []*pb.FailedDocRow {
	if len(allRows) == 0 {
		return nil
	}

	const batchSize = 100 // 批量处理的最大行数

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to begin transaction for static upsert: %v", err)
		return buildFailedDocRows(allRows, err)
	}
	defer rollbackTx(ctx, tx, "static upsert")

	// 分批处理
	for i := 0; i < len(allRows); i += batchSize {
		end := min(i+batchSize, len(allRows))
		batch := allRows[i:end]
		_, err := d.executeBatchStaticUpsert(ctx, tx, tableID, batch)
		if err != nil {
			log.ErrorContextf(ctx, "executeBatchStaticUpsert ERR:%s:%+v", tableID, err)
			return buildFailedDocRows(allRows, err)
		}
	}

	if err := tx.Commit(); err != nil {
		log.ErrorContextf(ctx, "Failed to commit static upsert transaction: %v", err)
		return buildFailedDocRows(allRows, err)
	}
	return nil
}

// executeBatchStaticUpsert 执行批量静态数据 upsert
func (d *DuckDB) executeBatchStaticUpsert(ctx context.Context, execer sqlExecer, tableID string, batch []*docInfo) ([]*docInfo, error) {
	if len(batch) == 0 {
		return nil, nil
	}

	// 收集INSERT需要的字段（用户字段+必要系统字段）
	columns := d.collectInsertColumns(ctx, batch)

	// 构建插入数据
	placeholders, allArgs := d.buildInsertData(ctx, batch, columns)

	// 构建 upsert 语句
	query := d.buildStaticUpsertQuery(tableID, columns, placeholders, nil)

	log.DebugContextf(ctx, "executeBatchStaticUpsert 执行 upsert SQL: %s, args: %v", query, allArgs)

	// 执行 upsert
	if _, err := execer.ExecContext(ctx, query, allArgs...); err != nil {
		log.ErrorContextf(ctx, "executeBatchStaticUpsert 批量 upsert 失败: %v, SQL: %s, args: %v", err, query, allArgs)
		return batch, errs.New(int(pb.EnumErrorCode_FAILED_UPDATE),
			fmt.Sprintf("executeBatchStaticUpsert Failed to batch upsert: %v; query: %s", err, query))
	}
	return nil, nil
}

// buildStaticUpsertQuery 构建静态数据的 upsert 查询语句
func (d *DuckDB) buildStaticUpsertQuery(tableID string, columns []string, placeholders []string, userColumns []string) string {
	// 构建基本的 INSERT 语句
	insertPart := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
		tableID,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "))

	// 构建 ON CONFLICT 子句，静态数据使用 _row_id 作为冲突检测字段
	conflictPart := "ON CONFLICT (_row_id)"

	updatePart := "DO NOTHING"

	// 组合完整的 upsert 语句
	return fmt.Sprintf("%s %s %s", insertPart, conflictPart, updatePart)
}

// fetchHistoricalData 获取历史数据
func (d *DuckDB) fetchHistoricalData(ctx context.Context, tableID string,
	historicalRowsLimit uint32) ([]*pb.DocRow, error) {
	if historicalRowsLimit == 0 {
		return nil, nil
	}

	historicalRows, err := d.getHistoricalRows(ctx, tableID, historicalRowsLimit)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to fetch historical rows: %v", err)
		return nil, err
	}
	return historicalRows, nil
}

// executeInsertOperations 执行插入操作
func (d *DuckDB) executeInsertOperations(ctx context.Context, tableID string,
	insertRows []*docInfo) []*pb.FailedDocRow {
	if len(insertRows) == 0 {
		return nil
	}

	failedRows, err := d.batchInsertRows(ctx, tableID, insertRows)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to batch insert rows: %v", err)
		var failedDocRows []*pb.FailedDocRow
		for _, info := range failedRows {
			failedDocRows = append(failedDocRows, createFailedDocRow(info.updateRow, err))
		}
		return failedDocRows
	}
	return nil
}

// executeUpdateOperations 执行更新操作
func (d *DuckDB) executeUpdateOperations(ctx context.Context, tableID string,
	updateRows []*docInfo) []*pb.FailedDocRow {
	if len(updateRows) == 0 {
		return nil
	}

	failedRows, err := d.batchUpdateRows(ctx, tableID, updateRows)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to batch update rows: %v", err)
		var failedDocRows []*pb.FailedDocRow
		for _, info := range failedRows {
			failedDocRows = append(failedDocRows, createFailedDocRow(info.updateRow, err))
		}
		return failedDocRows
	}
	return nil
}

// convertModifyInfosFromMap 从map转换修改信息为切片
func (d *DuckDB) convertModifyInfosFromMap(infos map[uint32]*pb.ModifyFieldInfo) []*pb.ModifyFieldInfo {
	var modifyInfos []*pb.ModifyFieldInfo
	for _, modifyInfo := range infos {
		modifyInfos = append(modifyInfos, modifyInfo)
	}
	return modifyInfos
}

// batchGetStaticDocRows 批量获取静态数据的当前值
func (d *DuckDB) batchGetStaticDocRows(ctx context.Context, tableID string, rowIDs []string,
	rowMap map[string]*docInfo) (map[string]map[uint32]*pb.FieldInfo, error) {
	if len(rowIDs) == 0 {
		return make(map[string]map[uint32]*pb.FieldInfo), nil
	}

	// 构建查询参数
	queryParams, err := d.prepareQueryParams(ctx, rowIDs, rowMap)
	if err != nil {
		return nil, err
	}

	// 执行数据库查询
	rows, queryColumns, err := d.executeQueryForDocRows(ctx, tableID, queryParams, "_row_id")
	if err != nil {
		if rows != nil {
			_ = rows.Close()
		}
		return nil, err
	}

	// 处理查询结果
	return d.processQueryResults(ctx, rows, queryColumns, queryParams.fieldIDMap, queryParams.fieldTypeMap, "_row_id")
}

// executeQueryForDocRows 执行数据库查询并返回结果
func (d *DuckDB) executeQueryForDocRows(ctx context.Context,
	tableID string, params *queryParams, idColumnName string) (*sql.Rows, []string, error) {
	// 构建查询语句
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s IN (%s)",
		strings.Join(params.columns, ", "),
		tableID,
		idColumnName,
		strings.Join(params.placeholders, ","))
	log.DebugContextf(ctx, "执行查询SQL: %s, args: %v", query, params.args)

	// 执行查询
	rows, err := d.db.QueryContext(ctx, query, params.args...)
	if err != nil {
		log.ErrorContextf(ctx, "查询失败: %v, SQL: %s, args: %v", err, query, params.args)
		return nil, nil, errs.New(int(pb.EnumErrorCode_FAILED_SELECT),
			fmt.Sprintf("Failed to execute query: %v", err))
	}

	// 获取列名
	queryColumns, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return nil, nil, errs.New(int(pb.EnumErrorCode_FAILED_SELECT), fmt.Sprintf("Failed to get column names: %v", err))
	}
	return rows, queryColumns, nil
}

// processQueryResults 处理查询结果并填充返回数据
func (d *DuckDB) processQueryResults(
	ctx context.Context,
	rows *sql.Rows,
	queryColumns []string,
	fieldIDMap map[string]uint32,
	fieldTypeMap map[string]pb.EnumFieldType,
	idColumnName string,
) (map[string]map[uint32]*pb.FieldInfo, error) {
	if rows == nil {
		return nil, nil
	}
	defer rows.Close()
	result := make(map[string]map[uint32]*pb.FieldInfo)

	// 创建行处理参数
	params := &rowProcessParams{
		queryColumns: queryColumns,
		fieldIDMap:   fieldIDMap,
		fieldTypeMap: fieldTypeMap,
		idColumnName: idColumnName,
	}

	// 遍历结果集
	for rows.Next() {
		rowData, err := d.scanRowData(ctx, rows, queryColumns)
		if err != nil {
			continue
		}

		rowID := d.extractRowID(rowData, queryColumns, idColumnName)
		if rowID == "" {
			continue
		}

		// 处理行字段
		d.processQueryRowFields(ctx, rowData, params, rowID, result)
	}
	return result, nil
}

// scanRowData 扫描行数据
func (d *DuckDB) scanRowData(ctx context.Context, rows *sql.Rows, queryColumns []string) ([]any, error) {
	values := make([]any, len(queryColumns))
	valuePtrs := make([]any, len(values))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	if err := rows.Scan(valuePtrs...); err != nil {
		log.ErrorContextf(ctx, "Failed to scan row: %v", err)
		return nil, err
	}
	return values, nil
}

// extractRowID 提取行ID
func (d *DuckDB) extractRowID(values []any, queryColumns []string, idColumnName string) string {
	for i, col := range queryColumns {
		if col == idColumnName {
			if idVal, ok := values[i].(string); ok {
				return idVal
			}
		}
	}
	return ""
}

// processQueryRowFields 处理查询行字段
func (d *DuckDB) processQueryRowFields(ctx context.Context, values []any, params *rowProcessParams, rowID string,
	result map[string]map[uint32]*pb.FieldInfo) {
	// 初始化该行的字段映射
	if result[rowID] == nil {
		result[rowID] = make(map[uint32]*pb.FieldInfo)
	}

	// 处理各个字段
	for i, col := range params.queryColumns {
		if col == params.idColumnName {
			continue
		}

		// 获取字段ID
		var fieldID uint32
		if id, exists := params.fieldIDMap[col]; exists {
			fieldID = id
		} else {
			fieldID = d.parseFieldID(ctx, d.tableID, col)
			if fieldID == 0 {
				log.WarnContextf(ctx, "Failed to parse column name: %s", col)
				continue
			}
		}

		// 转换字段值
		fieldParams := &fieldValueParams{
			fieldID:   fieldID,
			fieldType: params.fieldTypeMap[col],
			value:     values[i],
			mapKeys:   nil,
		}
		result[rowID][fieldID] = d.transFieldValue(ctx, fieldParams)
	}
}

// queryParams 存储查询参数
type queryParams struct {
	columns      []string
	args         []any
	placeholders []string
	fieldIDMap   map[string]uint32
	fieldTypeMap map[string]pb.EnumFieldType
}

// rowProcessParams 行处理参数
type rowProcessParams struct {
	queryColumns []string
	fieldIDMap   map[string]uint32
	fieldTypeMap map[string]pb.EnumFieldType
	idColumnName string
}

// prepareQueryParams 准备查询参数
func (d *DuckDB) prepareQueryParams(ctx context.Context, rowIDs []string, rowMap map[string]*docInfo) (*queryParams, error) {
	params := &queryParams{
		columns:      []string{"_row_id"}, // 始终包含_row_id
		args:         []any{},
		placeholders: []string{},
		fieldIDMap:   make(map[string]uint32),           // 字段名到ID的映射
		fieldTypeMap: make(map[string]pb.EnumFieldType), // 字段名到类型的映射
	}

	// 构建IN条件的占位符和参数
	for _, rowID := range rowIDs {
		params.placeholders = append(params.placeholders, "?")
		params.args = append(params.args, rowID)
	}

	// 构建查询所有需要的字段
	allFields := make(map[uint32]struct{})
	for _, info := range rowMap {
		for _, fieldID := range info.fieldIDs {
			allFields[fieldID] = struct{}{}
		}
	}

	// 获取所有字段名，使用统一的字段名格式函数
	for fieldID := range allFields {
		fieldType, err := d.getFieldType(ctx, fieldID)
		if err != nil {
			log.WarnContextf(ctx, "Skip field %d: %v", fieldID, err)
			continue
		}
		columnName := d.formatColumnName(ctx, d.tableID, fieldID)
		if columnName == "" {
			log.DebugContextf(ctx, "Skip field %d: formatColumnName returned empty string (不会导致插入失败，只会忽略该字段)", fieldID)
			continue
		}
		params.columns = append(params.columns, columnName)
		params.fieldIDMap[columnName] = fieldID
		params.fieldTypeMap[columnName] = fieldType
	}
	return params, nil
}

// batchInsertRows 批量插入行
func (d *DuckDB) batchInsertRows(ctx context.Context, tableID string, insertRows []*docInfo) ([]*docInfo, error) {
	if len(insertRows) == 0 {
		return nil, nil
	}
	const batchSize = 100     // 批量插入的最大行数
	var failedRows []*docInfo // 记录失败的行

	// 分批处理
	for i := 0; i < len(insertRows); i += batchSize {
		end := min(i+batchSize, len(insertRows))
		batch := insertRows[i:end]
		failedBatch, err := d.executeBatchInsert(ctx, tableID, batch)
		if err != nil {
			log.ErrorContextf(ctx, "executeBatchInsert ERR:%s:%+v", tableID, err)
		}
		failedRows = append(failedRows, failedBatch...)
	}

	if len(failedRows) > 0 {
		return failedRows, fmt.Errorf("some rows failed to insert")
	}
	return nil, nil
}

// executeBatchInsert 执行批量插入
func (d *DuckDB) executeBatchInsert(ctx context.Context, tableID string, batch []*docInfo) ([]*docInfo, error) {
	if len(batch) == 0 {
		return nil, nil
	}

	// 收集INSERT需要的字段
	columns := d.collectInsertColumns(ctx, batch)

	// 构建插入数据
	placeholders, allArgs := d.buildInsertData(ctx, batch, columns)

	// 执行批量插入
	return d.executeInsertQuery(ctx, tableID, columns, placeholders, allArgs, batch)
}

// collectUserColumns 收集用户实际要更新的字段列（不包括系统字段）
func (d *DuckDB) collectUserColumns(ctx context.Context, batch []*docInfo) []string {
	allFields := make(map[string]struct{})

	// 只收集用户实际要更新的字段
	for _, info := range batch {
		for fieldID := range info.insertFields {
			if _, err := d.getFieldType(ctx, fieldID); err != nil {
				continue
			}
			columnName := d.formatColumnName(ctx, d.tableID, fieldID)
			if columnName != "" {
				allFields[columnName] = struct{}{}
			}
		}
	}

	// 转换为有序字段列表
	columns := tinyfunc.Keys(allFields)
	sort.Strings(columns)
	return columns
}

// collectInsertColumns 收集INSERT操作需要的字段列（用户字段+必要系统字段）
func (d *DuckDB) collectInsertColumns(ctx context.Context, batch []*docInfo) []string {
	allFields := make(map[string]struct{})

	// 收集用户实际提供的字段
	for _, info := range batch {
		for fieldID := range info.insertFields {
			if _, err := d.getFieldType(ctx, fieldID); err != nil {
				continue
			}
			columnName := d.formatColumnName(ctx, d.tableID, fieldID)
			if columnName != "" {
				allFields[columnName] = struct{}{}
			}
		}
	}

	// 只包含必要的系统字段
	necessarySystemFields := []string{"_row_id", "_times", "_ctime", "_mtime", "_deleted", "_replay_timestamps"}
	for _, field := range necessarySystemFields {
		allFields[field] = struct{}{}
	}

	// 转换为有序字段列表
	columns := tinyfunc.Keys(allFields)
	sort.Strings(columns)
	return columns
}

// buildInsertData 构建插入数据
func (d *DuckDB) buildInsertData(ctx context.Context, batch []*docInfo, columns []string) ([]string, []any) {
	var placeholders []string
	var allArgs []any

	for _, info := range batch {
		// 构建占位符
		rowPlaceholders := make([]string, len(columns))
		for i := range columns {
			rowPlaceholders[i] = "?"
		}
		placeholders = append(placeholders, fmt.Sprintf("(%s)", strings.Join(rowPlaceholders, ", ")))

		// 准备行数据
		rowData := d.prepareRowData(ctx, info)

		// 按列顺序添加参数
		for _, col := range columns {
			val, exists := rowData[col]
			if !exists {
				val = nil
			}
			allArgs = append(allArgs, val)
		}
	}
	return placeholders, allArgs
}

// prepareRowData 准备行数据
func (d *DuckDB) prepareRowData(ctx context.Context, info *docInfo) map[string]any {
	rowData := make(map[string]any)

	// 添加系统字段
	d.addSystemFields(rowData, info)

	// 处理用户字段
	d.addUserFields(ctx, rowData, info)
	return rowData
}

// addSystemFields 添加系统字段
func (d *DuckDB) addSystemFields(rowData map[string]any, info *docInfo) {
	currentTime := utils.GetCurrentTimeStandard()
	updateTimes := info.updateRow.Times
	if updateTimes == "" {
		updateTimes = currentTime
	}
	// 标准化_times字段格式
	updateTimes = utils.NormalizeTimeString(updateTimes)

	// 设置固定的回放时间戳，使用普通时间格式
	replayTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05")

	rowData["_row_id"] = info.updateRow.RowId
	rowData["_times"] = updateTimes
	rowData["_ctime"] = currentTime
	rowData["_mtime"] = currentTime
	rowData["_replay_timestamps"] = replayTime
	rowData["_deleted"] = 0
}

// addUserFields 添加用户字段
func (d *DuckDB) addUserFields(ctx context.Context, rowData map[string]any, info *docInfo) {
	for fieldID, fieldInfo := range info.insertFields {
		if _, err := d.getFieldType(ctx, fieldID); err != nil {
			continue
		}

		columnName := d.formatColumnName(ctx, d.tableID, fieldID)
		if columnName == "" {
			log.DebugContextf(ctx, "Skip field %d: formatColumnName returned empty string (不会导致插入失败，只会忽略该字段)", fieldID)
			continue
		}

		value := d.convertFieldValue(ctx, fieldInfo)
		if value != nil {
			rowData[columnName] = value
		}
	}
}

// convertFieldValue 转换字段值
func (d *DuckDB) convertFieldValue(ctx context.Context, fieldInfo *pb.FieldInfo) any {
	switch fieldInfo.FieldType {
	case pb.EnumFieldType_STR_FIELD:
		return fieldInfo.SimpleValue.GetStr()
	case pb.EnumFieldType_INT_FIELD:
		return fieldInfo.SimpleValue.GetInt()
	case pb.EnumFieldType_FLOAT_FIELD:
		return fieldInfo.SimpleValue.GetFloat()
	case pb.EnumFieldType_TIME_FIELD:
		return fieldInfo.SimpleValue.GetTime()
	case pb.EnumFieldType_INT_VEC_FIELD:
		jsonStr, _ := json.Marshal(fieldInfo.SimpleValue.GetIntList().GetValues())
		return string(jsonStr)
	case pb.EnumFieldType_SET_FIELD:
		jsonStr, _ := json.Marshal(fieldInfo.SimpleValue.GetStrList().GetValues())
		return string(jsonStr)
	case pb.EnumFieldType_MAP_KV_FIELD, pb.EnumFieldType_MAP_KLIST_FIELD:
		jsonStr, _ := json.Marshal(fieldInfo.MapValue.GetEntries())
		return string(jsonStr)
	default:
		log.WarnContextf(ctx, "Unsupported field type: %v", fieldInfo.FieldType)
		return nil
	}
}

// executeInsertQuery 执行插入查询
func (d *DuckDB) executeInsertQuery(ctx context.Context, tableID string, columns []string,
	placeholders []string, allArgs []any, batch []*docInfo) ([]*docInfo, error) {
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
		tableID,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "))

	log.InfoContextf(ctx, "executeInsertQuery 执行插入SQL: %s, args: %v", query, allArgs)

	if _, err := d.db.ExecContext(ctx, query, allArgs...); err != nil {
		log.ErrorContextf(ctx, "executeInsertQuery 批量插入失败: %v, SQL: %s, args: %v", err, query, allArgs)
		return batch, errs.New(int(pb.EnumErrorCode_FAILED_UPDATE),
			fmt.Sprintf("executeInsertQuery Failed to batch insert: %v; query: %s", err, query))
	}
	return nil, nil
}

// batchUpdateRows 批量更新行
func (d *DuckDB) batchUpdateRows(ctx context.Context, tableID string, updateRows []*docInfo) ([]*docInfo, error) {
	if len(updateRows) == 0 {
		return nil, nil
	}

	// 记录失败的行
	var failedRows []*docInfo

	// 由于更新操作的条件可能不同，逐行处理
	for _, info := range updateRows {
		if len(info.setParts) == 0 {
			continue
		}

		// 添加_mtime到更新字段，确保每次更新都记录当前时间
		currentTime := utils.GetCurrentTimeStandard()
		info.setParts = append(info.setParts, "_mtime = ?")
		info.args = append(info.args, currentTime)

		// 构建更新语句
		query := fmt.Sprintf("UPDATE %s SET %s WHERE _row_id = ?",
			tableID,
			strings.Join(info.setParts, ", "))

		// 添加行ID参数
		info.args = append(info.args, info.updateRow.RowId)

		// 处理_times条件（如适用）
		if info.updateRow.Times != "" {
			query += " AND _times = ?"
			info.args = append(info.args, info.updateRow.Times)
		}

		// 打印实际执行的SQL语句和参数，方便定位问题
		log.InfoContextf(ctx, "执行更新SQL: %s, args: %v", query, info.args)

		// 执行更新
		_, err := d.db.ExecContext(ctx, query, info.args...)
		if err != nil {
			log.ErrorContextf(ctx, "更新失败: %v, SQL: %s, args: %v", err, query, info.args)
			failedRows = append(failedRows, info)
		}
	}

	if len(failedRows) > 0 {
		return failedRows, errs.New(int(pb.EnumErrorCode_FAILED_UPDATE), "Failed to update some rows")
	}
	return nil, nil
}

// generateRowIDFromTime 从时间字符串生成行ID
// 尝试解析时间并转换为时间戳字符串，如果失败则使用原RowId
func (d *DuckDB) generateRowIDFromTime(timeStr string) string {
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

// preprocessTimingRows 预处理时序数据行
func (d *DuckDB) preprocessTimingRows(ctx context.Context,
	updateDocRows []*pb.UpdateDocRow) map[string]*docInfo {
	timeMap := make(map[string]*docInfo)

	for _, updateRow := range updateDocRows {
		// 对于时序数据，使用times中的时间作为行ID（转换为时间戳）
		rowID := d.generateRowIDFromTime(updateRow.Times)
		if rowID == "" {
			// 如果时间转换失败，检查RowId是否为空，如果为空则生成唯一ID
			if updateRow.RowId == "" {
				updateRow.RowId = helper.GenRowID()
				log.InfoContextf(ctx, "Generated unique ID for row: %s", updateRow.RowId)
			}
			rowID = updateRow.RowId
		} else {
			// 更新updateRow的RowId为转换后的时间戳
			updateRow.RowId = rowID
			log.DebugContextf(ctx, "Generated timestamp-based row ID: %s for time: %s", rowID, updateRow.Times)
		}

		// 收集行信息
		info := &docInfo{
			updateRow: updateRow,
			fieldIDs:  tinyfunc.Keys(updateRow.Fields),
			docRow: &pb.DocRow{
				RowId:  rowID, // 使用转换后的时间戳字符串作为行ID
				Times:  updateRow.Times,
				Fields: make(map[uint32]*pb.FieldInfo),
			},
			setParts:     []string{},
			args:         []any{},
			insertFields: make(map[uint32]*pb.FieldInfo),
			failedList:   make(map[uint32]*pb.FailedInfo),
		}
		timeMap[updateRow.Times] = info
	}
	return timeMap
}

// prepareTimingRowsForUpsert 为时序数据准备 upsert 操作
func (d *DuckDB) prepareTimingRowsForUpsert(ctx context.Context,
	timeMap map[string]*docInfo) ([]*docInfo, []*pb.FailedDocRow, map[uint32]*pb.ModifyFieldInfo) {
	var allRows []*docInfo
	var failedDocRows []*pb.FailedDocRow
	allInfos := make(map[uint32]*pb.ModifyFieldInfo) // 收集所有修改信息

	// 处理每一行
	for _, info := range timeMap {
		// 处理字段更新并收集修改信息（时序数据不需要当前值）
		rowInfos := d.processTimingRowFields(info)

		// 合并修改信息
		for fieldID, modifyInfo := range rowInfos {
			allInfos[fieldID] = modifyInfo
		}

		// 如果有有效字段，添加到处理列表
		if len(info.insertFields) > 0 {
			allRows = append(allRows, info)
		}

		// 处理失败的字段
		if len(info.failedList) > 0 {
			failedDocRow := &pb.FailedDocRow{
				Times:      info.updateRow.Times,
				RowId:      info.updateRow.RowId,
				FailedList: info.failedList,
			}
			failedDocRows = append(failedDocRows, failedDocRow)
		}
	}
	return allRows, failedDocRows, allInfos
}

// processTimingRowFields 处理时序数据行字段更新（不需要当前值）
func (d *DuckDB) processTimingRowFields(info *docInfo) map[uint32]*pb.ModifyFieldInfo {
	infos := make(map[uint32]*pb.ModifyFieldInfo)
	for fieldID, updateInfo := range info.updateRow.Fields {
		// 时序数据只支持 SET_UPDATE 操作，因为过去的数据不会修改
		if updateInfo.UpdateType != pb.EnumUpdateType_SET_UPDATE {
			info.failedList[fieldID] = &pb.FailedInfo{
				Code: pb.EnumErrorCode_INVALID_OP_TYPE,
				Msg:  fmt.Sprintf("Timing data only supports SET_UPDATE operation, got: %v", updateInfo.UpdateType),
			}
			continue
		}

		// 生成修改信息（时序数据的变更通知只包含当前数据信息）
		modifyInfo := &pb.ModifyFieldInfo{
			NewDocRow: &pb.DocRow{
				Times: info.updateRow.Times,
				Fields: map[uint32]*pb.FieldInfo{
					fieldID: updateInfo.FieldInfo,
				},
			},
		}
		infos[fieldID] = modifyInfo

		// 准备字段数据用于插入
		info.insertFields[fieldID] = updateInfo.FieldInfo

		// 添加到结果
		if updateInfo.FieldInfo != nil {
			info.docRow.Fields[fieldID] = updateInfo.FieldInfo
		}
	}
	return infos
}

// executeTimingUpsertOperations 执行时序数据的 upsert 操作
func (d *DuckDB) executeTimingUpsertOperations(ctx context.Context, tableID string,
	allRows []*docInfo) []*pb.FailedDocRow {
	if len(allRows) == 0 {
		return nil
	}

	const batchSize = 100 // 批量处理的最大行数

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to begin transaction for timing upsert: %v", err)
		return buildFailedDocRows(allRows, err)
	}
	defer rollbackTx(ctx, tx, "timing upsert")

	// 分批处理
	for i := 0; i < len(allRows); i += batchSize {
		end := min(i+batchSize, len(allRows))
		batch := allRows[i:end]
		_, err := d.executeBatchTimingUpsert(ctx, tx, tableID, batch)
		if err != nil {
			log.ErrorContextf(ctx, "executeBatchTimingUpsert ERR:%s:%+v", tableID, err)
			return buildFailedDocRows(allRows, err)
		}
	}

	if err := tx.Commit(); err != nil {
		log.ErrorContextf(ctx, "Failed to commit timing upsert transaction: %v", err)
		return buildFailedDocRows(allRows, err)
	}
	return nil
}

// executeBatchTimingUpsert 执行批量时序数据 upsert
func (d *DuckDB) executeBatchTimingUpsert(ctx context.Context, execer sqlExecer, tableID string, batch []*docInfo) ([]*docInfo, error) {
	if len(batch) == 0 {
		return nil, nil
	}

	// 收集INSERT需要的字段（用户字段+必要系统字段）
	columns := d.collectInsertColumns(ctx, batch)

	// 构建插入数据
	placeholders, allArgs := d.buildInsertData(ctx, batch, columns)

	// 构建 upsert 语句
	query := d.buildTimingUpsertQuery(tableID, columns, placeholders, nil)

	log.DebugContextf(ctx, "executeBatchTimingUpsert 执行 upsert SQL: %s, args: %v", query, allArgs)

	// 执行 upsert
	if _, err := execer.ExecContext(ctx, query, allArgs...); err != nil {
		log.ErrorContextf(ctx, "executeBatchTimingUpsert 批量 upsert 失败: %v, SQL: %s, args: %v", err, query, allArgs)
		return batch, errs.New(int(pb.EnumErrorCode_FAILED_UPDATE),
			fmt.Sprintf("executeBatchTimingUpsert Failed to batch upsert: %v; query: %s", err, query))
	}
	return nil, nil
}

// buildTimingUpsertQuery 构建时序数据的 upsert 查询语句
func (d *DuckDB) buildTimingUpsertQuery(tableID string, columns []string, placeholders []string, userColumns []string) string {
	// 构建基本的 INSERT 语句
	insertPart := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
		tableID,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "))

	// 构建 ON CONFLICT 子句，时序数据使用 _times 作为冲突检测字段
	conflictPart := "ON CONFLICT (_times)"

	updatePart := "DO NOTHING"

	// 组合完整的 upsert 语句
	return fmt.Sprintf("%s %s %s", insertPart, conflictPart, updatePart)
}

// batchGetTimingDocRows 批量获取时序数据的当前值
func (d *DuckDB) batchGetTimingDocRows(ctx context.Context, tableID string,
	timeKeys []string, timeMap map[string]*docInfo) (map[string]map[uint32]*pb.FieldInfo, error) {
	if len(timeKeys) == 0 {
		return make(map[string]map[uint32]*pb.FieldInfo), nil
	}

	// 构建查询参数
	queryParams, err := d.prepareTimingQueryParams(ctx, timeKeys, timeMap)
	if err != nil {
		return nil, err
	}

	// 执行数据库查询
	rows, queryColumns, err := d.executeQueryForDocRows(ctx, tableID, queryParams, "_times")
	if err != nil {
		if rows != nil {
			rows.Close()
		}
		return nil, err
	}

	// 处理查询结果
	return d.processQueryResults(ctx, rows, queryColumns, queryParams.fieldIDMap, queryParams.fieldTypeMap, "_times")
}

// prepareTimingQueryParams 准备时序数据查询参数
func (d *DuckDB) prepareTimingQueryParams(ctx context.Context, timeKeys []string, timeMap map[string]*docInfo) (*queryParams, error) {
	params := &queryParams{
		columns:      []string{"_times"}, // 始终包含_times
		args:         []any{},
		placeholders: []string{},
		fieldIDMap:   make(map[string]uint32),           // 字段名到ID的映射
		fieldTypeMap: make(map[string]pb.EnumFieldType), // 字段名到类型的映射
	}

	// 构建IN条件的占位符和参数
	for _, timeKey := range timeKeys {
		params.placeholders = append(params.placeholders, "?")
		params.args = append(params.args, timeKey)
	}

	// 构建查询所有需要的字段
	allFields := make(map[uint32]struct{})
	for _, info := range timeMap {
		for _, fieldID := range info.fieldIDs {
			allFields[fieldID] = struct{}{}
		}
	}

	// 获取所有字段名，使用统一的字段名格式函数
	for fieldID := range allFields {
		fieldType, err := d.getFieldType(ctx, fieldID)
		if err != nil {
			log.WarnContextf(ctx, "Skip field %d: %v", fieldID, err)
			continue
		}
		columnName := d.formatColumnName(ctx, d.tableID, fieldID)
		if columnName == "" {
			log.DebugContextf(ctx, "Skip field %d: formatColumnName returned empty string (不会导致插入失败，只会忽略该字段)", fieldID)
			continue
		}
		params.columns = append(params.columns, columnName)
		params.fieldIDMap[columnName] = fieldID
		params.fieldTypeMap[columnName] = fieldType
	}
	return params, nil
}

// processFieldUpdate 处理单个字段的更新操作（返回：更新后的值，修改变更信息，错误）
func (d *DuckDB) processFieldUpdate(updateInfo *pb.UpdateFieldInfo,
	currentValue *pb.FieldInfo, fieldType pb.EnumFieldType) (any, *pb.ModifyFieldInfo, error) {
	// 确保currentValue不为空
	if currentValue == nil {
		currentValue = &pb.FieldInfo{
			FieldId:   updateInfo.FieldInfo.FieldId,
			FieldType: fieldType,
		}
	}

	// 生成修改变更信息
	modifyInfo := genModifyInfo(updateInfo.FieldInfo.FieldId, currentValue, updateInfo.FieldInfo)

	// 根据更新类型选择处理方式
	var result any
	var err error
	switch updateInfo.UpdateType {
	case pb.EnumUpdateType_SET_UPDATE:
		result, err = d.handleSetUpdate(updateInfo.FieldInfo, fieldType)
	case pb.EnumUpdateType_DEL_UPDATE:
		result, err = d.handleDelUpdate(currentValue, updateInfo.FieldInfo, fieldType)
	case pb.EnumUpdateType_APPEND_UPDATE:
		result, err = d.handleAppendUpdate(currentValue, updateInfo.FieldInfo, fieldType)
	default:
		return nil, nil, fmt.Errorf("unsupported update type: %v", updateInfo.UpdateType)
	}

	if err != nil {
		return nil, nil, err
	}
	return result, modifyInfo, nil
}

// genModifyInfo 生成修改信息
func genModifyInfo(fieldID uint32, oldValue, newValue *pb.FieldInfo) *pb.ModifyFieldInfo {
	return &pb.ModifyFieldInfo{
		OldDocRow: &pb.DocRow{
			Fields: map[uint32]*pb.FieldInfo{
				fieldID: oldValue,
			},
		},
		NewDocRow: &pb.DocRow{
			Fields: map[uint32]*pb.FieldInfo{
				fieldID: newValue,
			},
		},
	}
}

// handleSetUpdate 处理覆盖更新操作
func (d *DuckDB) handleSetUpdate(fieldInfo *pb.FieldInfo, fieldType pb.EnumFieldType) (any, error) {
	switch fieldType {
	case pb.EnumFieldType_STR_FIELD:
		return fieldInfo.SimpleValue.GetStr(), nil
	case pb.EnumFieldType_INT_FIELD:
		return fieldInfo.SimpleValue.GetInt(), nil
	case pb.EnumFieldType_FLOAT_FIELD:
		return fieldInfo.SimpleValue.GetFloat(), nil
	case pb.EnumFieldType_TIME_FIELD:
		return fieldInfo.SimpleValue.GetTime(), nil
	case pb.EnumFieldType_INT_VEC_FIELD:
		jsonStr, err := json.Marshal(fieldInfo.SimpleValue.GetIntList().GetValues())
		if err != nil {
			return nil, err
		}
		return string(jsonStr), nil
	case pb.EnumFieldType_SET_FIELD:
		jsonStr, err := json.Marshal(fieldInfo.SimpleValue.GetStrList().GetValues())
		if err != nil {
			return nil, err
		}
		return string(jsonStr), nil
	case pb.EnumFieldType_MAP_KV_FIELD, pb.EnumFieldType_MAP_KLIST_FIELD:
		jsonStr, err := json.Marshal(fieldInfo.MapValue.GetEntries())
		if err != nil {
			return nil, err
		}
		return string(jsonStr), nil
	default:
		return nil, fmt.Errorf("unsupported field type: %v", fieldType)
	}
}

// handleDelUpdate 处理删除更新操作
func (d *DuckDB) handleDelUpdate(currentValue, newValue *pb.FieldInfo, fieldType pb.EnumFieldType) (any, error) {
	switch fieldType {
	case pb.EnumFieldType_INT_VEC_FIELD:
		return d.handleIntVecDeletion(currentValue, newValue)
	case pb.EnumFieldType_SET_FIELD:
		return d.handleSetDeletion(currentValue, newValue)
	case pb.EnumFieldType_MAP_KV_FIELD, pb.EnumFieldType_MAP_KLIST_FIELD:
		return d.handleMapDeletion(currentValue, newValue)
	default:
		return nil, fmt.Errorf("unsupported field type: %v", fieldType)
	}
}

// handleIntVecDeletion 处理整型向量删除
func (d *DuckDB) handleIntVecDeletion(currentValue, newValue *pb.FieldInfo) (any, error) {
	result := tinyfunc.SliceDifference(currentValue.SimpleValue.GetIntList().GetValues(),
		newValue.SimpleValue.GetIntList().GetValues())
	jsonStr, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return string(jsonStr), nil
}

// handleSetDeletion 处理集合删除
func (d *DuckDB) handleSetDeletion(currentValue, newValue *pb.FieldInfo) (any, error) {
	result := tinyfunc.SliceDifference(currentValue.SimpleValue.GetStrList().GetValues(),
		newValue.SimpleValue.GetStrList().GetValues())
	jsonStr, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return string(jsonStr), nil
}

// handleMapDeletion 处理映射删除
func (d *DuckDB) handleMapDeletion(currentValue, newValue *pb.FieldInfo) (any, error) {
	// 从Map中删除指定的key
	currentMap := make(map[string]any)
	for k, v := range currentValue.MapValue.GetEntries() {
		currentMap[k] = v
	}

	newMap := make(map[string]any)
	for k, v := range newValue.MapValue.GetEntries() {
		newMap[k] = v
	}

	result := tinyfunc.MapDifference(currentMap, newMap)
	jsonStr, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return string(jsonStr), nil
}

// handleAppendUpdate 处理追加更新操作
func (d *DuckDB) handleAppendUpdate(currentValue, newValue *pb.FieldInfo, fieldType pb.EnumFieldType) (any, error) {
	switch fieldType {
	case pb.EnumFieldType_INT_VEC_FIELD:
		// 整型向量
		currentValues := currentValue.SimpleValue.GetIntList().GetValues()
		newValues := newValue.SimpleValue.GetIntList().GetValues()
		result := append(currentValues, newValues...)
		jsonStr, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		return string(jsonStr), nil
	case pb.EnumFieldType_SET_FIELD:
		// 集合类型
		currentValues := currentValue.SimpleValue.GetStrList().GetValues()
		newValues := newValue.SimpleValue.GetStrList().GetValues()
		result := appendUniqueStrings(currentValues, newValues)
		jsonStr, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		return string(jsonStr), nil
	case pb.EnumFieldType_MAP_KV_FIELD, pb.EnumFieldType_MAP_KLIST_FIELD:
		// 向Map添加或更新key-value
		result := d.mergeMapEntries(currentValue.MapValue.GetEntries(), newValue.MapValue.GetEntries())
		jsonStr, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		return string(jsonStr), nil
	default:
		return nil, fmt.Errorf("unsupported field type: %v", fieldType)
	}
}

// mergeMapEntries 合并两个map
func (d *DuckDB) mergeMapEntries(currentMap, newMap map[string]*pb.KeyValueEntry) map[string]*pb.KeyValueEntry {
	result := make(map[string]*pb.KeyValueEntry, len(currentMap)+len(newMap))
	maps.Copy(result, currentMap)
	maps.Copy(result, newMap)
	return result
}

// appendUniqueStrings 追加唯一字符串
func appendUniqueStrings(source, toAppend []string) []string {
	if len(toAppend) == 0 {
		return source
	}

	// 创建源数组元素的映射，用于O(1)查找
	exists := make(map[string]struct{})
	for _, e := range source {
		exists[e] = struct{}{}
	}

	// 添加新元素
	for _, e := range toAppend {
		if _, found := exists[e]; !found {
			source = append(source, e)
			exists[e] = struct{}{}
		}
	}
	return source
}

// 添加新函数：获取历史行数据
func (d *DuckDB) getHistoricalRows(ctx context.Context, tableID string, limit uint32) ([]*pb.DocRow, error) {
	if limit == 0 {
		return nil, nil
	}

	// 执行查询并获取列信息
	rows, columns, err := d.executeHistoricalQuery(ctx, tableID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 处理查询结果
	return d.processHistoricalRows(ctx, rows, columns)
}

// executeHistoricalQuery 执行历史数据查询
func (d *DuckDB) executeHistoricalQuery(ctx context.Context, tableID string, limit uint32) (*sql.Rows, []string, error) {
	query := fmt.Sprintf("SELECT * FROM %s ORDER BY _ctime DESC LIMIT ?", tableID)
	rows, err := d.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, nil, err
	}

	columns, err := rows.Columns()
	if err != nil {
		if rows != nil {
			rows.Close()
		}
		return nil, nil, err
	}
	return rows, columns, nil
}

// processHistoricalRows 处理历史数据行
func (d *DuckDB) processHistoricalRows(ctx context.Context, rows *sql.Rows, columns []string) ([]*pb.DocRow, error) {
	var result []*pb.DocRow
	for rows.Next() {
		docRow := &pb.DocRow{
			Fields: make(map[uint32]*pb.FieldInfo),
		}

		// 扫描行数据
		values, err := d.scanRowData(ctx, rows, columns)
		if err != nil {
			continue
		}

		// 处理列数据
		for i, col := range columns {
			switch col {
			case "_row_id":
				if rowID, ok := values[i].(string); ok {
					docRow.RowId = rowID
				}
			case "_times":
				if times, ok := values[i].(string); ok {
					docRow.Times = times
				}
			default:
				d.processUserField(ctx, docRow, col, values[i])
			}
		}
		result = append(result, docRow)
	}
	return result, nil
}

// processUserField 处理用户字段
func (d *DuckDB) processUserField(ctx context.Context, docRow *pb.DocRow, col string, value any) {
	fieldID := d.parseFieldID(ctx, d.tableID, col)
	if fieldID == 0 {
		log.WarnContextf(ctx, "Failed to parse column name %s", col)
		return
	}

	fieldType, err := d.getFieldType(ctx, fieldID)
	if err != nil {
		log.WarnContextf(ctx, "Skip field (%d): %v", fieldID, err)
		return
	}

	fieldParams := &fieldValueParams{
		fieldID:   fieldID,
		fieldType: fieldType,
		value:     value,
		mapKeys:   nil,
	}
	fieldInfo := d.transFieldValue(ctx, fieldParams)
	if fieldInfo != nil {
		docRow.Fields[fieldID] = fieldInfo
	}
}

// 创建失败文档行
func createFailedDocRow(updateRow *pb.UpdateDocRow, err error) *pb.FailedDocRow {
	failedRow := &pb.FailedDocRow{
		Times:      updateRow.Times,
		RowId:      updateRow.RowId,
		FailedList: make(map[uint32]*pb.FailedInfo),
	}

	// 为每个字段添加失败信息
	for fieldID := range updateRow.Fields {
		failedRow.FailedList[fieldID] = &pb.FailedInfo{
			Code: pb.EnumErrorCode_FAILED_UPDATE,
			Msg:  err.Error(),
		}
	}

	return failedRow
}

func buildFailedDocRows(rows []*docInfo, err error) []*pb.FailedDocRow {
	failedDocRows := make([]*pb.FailedDocRow, 0, len(rows))
	for _, info := range rows {
		failedDocRows = append(failedDocRows, createFailedDocRow(info.updateRow, err))
	}
	return failedDocRows
}

func rollbackTx(ctx context.Context, tx *sql.Tx, label string) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		log.ErrorContextf(ctx, "Failed to rollback %s transaction: %v", label, err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ensureTableExists 确保表存在，包含缓存逻辑
func (d *DuckDB) ensureTableExists(ctx context.Context, tableID string, dataType pb.EnumDataTypeCategory) error {
	resolvedType := dataType
	if inferredType, ok := helper.InferDataTypeFromTableID(tableID); ok {
		switch resolvedType {
		case pb.EnumDataTypeCategory_INVALID_DATA_TYPE_CATEGORY:
			log.WarnContextf(ctx, "自动创建表[%s]缺少数据类型，使用表名推断类型: %d", tableID, inferredType)
			resolvedType = inferredType
		case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
			if inferredType == pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE {
				log.WarnContextf(ctx, "自动创建表[%s]数据类型为静态，但表名推断为时序，使用时序类型", tableID)
				resolvedType = inferredType
			}
		}
	}

	// 生成缓存键
	cacheKey := d.generateTableCacheKey(tableID)

	// 检查缓存
	if d.checkTableCache(ctx, cacheKey) {
		return nil
	}

	// 检查表是否存在
	exists, err := d.CheckTable(ctx, tableID)
	if err != nil {
		log.ErrorContextf(ctx, "Failed to check table existence: %v", err)
		d.cacheTableResult(cacheKey, tableID, "failed")
		return err
	}
	if exists {
		// 表已存在，缓存结果
		d.cacheTableResult(cacheKey, tableID, "exists")
		return nil
	}

	// 表不存在，创建表
	if resolvedType != pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE &&
		resolvedType != pb.EnumDataTypeCategory_STATIC_DATA_TYPE {
		log.ErrorContextf(ctx, "自动创建表[%s]失败: 数据类型不能为空或非法: %d", tableID, resolvedType)
		d.cacheTableResult(cacheKey, tableID, "failed")
		return fmt.Errorf("invalid data type: %d", resolvedType)
	}
	createParams := &dao.CreateTableParams{
		TableID:     tableID,
		DataType:    resolvedType,
		ForceCreate: false,
	}
	if err := d.CreateTable(ctx, createParams); err != nil {
		log.ErrorContextf(ctx, "Failed to create table: %v", err)
		d.cacheTableResult(cacheKey, tableID, "failed")
		return err
	}

	// 创建成功，缓存结果
	d.cacheTableResult(cacheKey, tableID, "created")
	return nil
}

// generateTableCacheKey 生成表缓存键
func (d *DuckDB) generateTableCacheKey(tableID string) string {
	return fmt.Sprintf("duckdb_table:%s", tableID)
}

// checkTableCache 检查表缓存
func (d *DuckDB) checkTableCache(ctx context.Context, cacheKey string) bool {
	if cached, found := localcache.Get(cacheKey); found {
		if cacheValue, ok := cached.(*TableCacheValue); ok {
			if cacheValue.Status == "created" || cacheValue.Status == "exists" {
				log.DebugContextf(ctx, "Table exists in cache: %s", cacheKey)
				return true
			}
		}
	}
	return false
}

// cacheTableResult 缓存表创建结果
func (d *DuckDB) cacheTableResult(cacheKey string, tableID string, status string) {
	cacheValue := &TableCacheValue{
		TableID:    tableID,
		Status:     status,
		CreateTime: time.Now(),
	}
	// 缓存30分钟
	localcache.Set(cacheKey, cacheValue, 30*60)
}
