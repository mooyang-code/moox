//go:build !noduckdb && cgo
// +build !noduckdb,cgo

// Package duckdb DuckDB的工具函数
package duckdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/go-commlib/tinyfunc"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/helper"
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

// ============================================================================
// 辅助工具函数 - 底层工具和转换函数
// ============================================================================

// RowProcessor 行处理函数类型定义，用于处理查询结果的每一行
type RowProcessor func(docRow *pb.DocRow, columns []string, values []any)

// executeQueryWithRowProcessor 通用查询执行函数，支持自定义行处理器
func (d *DuckDB) executeQueryWithRowProcessor(ctx context.Context, query string, args []any,
	mapKeys map[uint32]*pb.KeyList, rowProcessor RowProcessor) ([]*pb.DocRow, error) {
	// 打印实际执行的SQL语句和参数，方便定位问题
	log.DebugContextf(ctx, "执行查询SQL: %s, args: %v", query, args)

	// 执行查询
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		// 检查是否是表不存在的错误
		if d.isTableNotExistError(err) {
			log.WarnContextf(ctx, "Table does not exist, returning empty result: %v", err)
			return []*pb.DocRow{}, nil
		}
		log.ErrorContextf(ctx, "查询失败: %v, SQL: %s, args: %v", err, query, args)
		return nil, errs.New(int(pb.EnumErrorCode_FAILED_SELECT), fmt.Sprintf("Failed to execute query: %v", err))
	}
	defer rows.Close()

	// 获取列名
	columns, err := rows.Columns()
	if err != nil {
		log.ErrorContextf(ctx, "Failed to get column names: %v", err)
		return nil, errs.New(int(pb.EnumErrorCode_FAILED_SELECT), fmt.Sprintf("Failed to get column names: %v", err))
	}

	// 遍历结果集
	var docRows []*pb.DocRow
	for rows.Next() {
		// 准备接收列值的容器
		var values = make([]any, len(columns))
		var valuePtrs = make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		// 扫描行数据到容器
		if err := rows.Scan(valuePtrs...); err != nil {
			log.ErrorContextf(ctx, "Failed to scan row: %v", err)
			continue
		}

		// 创建DocRow对象
		docRow := &pb.DocRow{
			Fields: make(map[uint32]*pb.FieldInfo),
		}

		// 允许自定义行处理
		if rowProcessor != nil {
			rowProcessor(docRow, columns, values)
		}

		// 处理各个字段
		for i, col := range columns {
			// 跳过_row_id和_times，因为它们已在行处理器(rowProcessor)中处理
			if col == "_row_id" || col == "_times" {
				continue
			}

			fieldID := d.parseFieldID(ctx, d.tableID, col)
			if fieldID == 0 {
				continue // 跳过未知字段
			}

			// 获取字段类型
			fieldType, err := d.getFieldType(ctx, fieldID)
			if err != nil {
				continue // 跳过获取失败的字段
			}

			// 添加到DocRow
			params := &fieldValueParams{
				fieldID:   fieldID,
				fieldType: fieldType,
				value:     values[i],
				mapKeys:   mapKeys,
			}
			docRow.Fields[fieldID] = d.transFieldValue(ctx, params)
		}
		docRows = append(docRows, docRow)
	}
	return docRows, nil
}

// buildFieldsList 构建查询字段列表
func (d *DuckDB) buildColumnNameList(fieldIDs []uint32) string {
	if d.isGetAll {
		return "*"
	}

	// 添加必要的ID字段和系统字段
	var colList []string
	for field := range constants.SystemFields {
		colList = append(colList, field)
	}

	// 添加用户请求的字段
	for _, fieldID := range fieldIDs {
		columnName := d.formatColumnName(context.Background(), d.tableID, fieldID)
		if columnName != "" && !constants.SystemFields[columnName] {
			colList = append(colList, columnName)
		}
	}

	if len(colList) == 0 {
		return "*"
	}
	return strings.Join(tinyfunc.RemoveDuplicates(colList), ", ")
}

// fieldValueParams 字段值转换参数
type fieldValueParams struct {
	fieldID   uint32
	fieldType pb.EnumFieldType
	value     any
	mapKeys   map[uint32]*pb.KeyList
}

// transFieldValue 将数据库原始值转换为FieldInfo结构体
func (d *DuckDB) transFieldValue(ctx context.Context, params *fieldValueParams) *pb.FieldInfo {
	if params.value == nil {
		return &pb.FieldInfo{
			FieldId:   params.fieldID,
			FieldType: params.fieldType,
		}
	}
	// 创建一个新的FieldInfo对象
	result := &pb.FieldInfo{
		FieldId:   params.fieldID,
		FieldType: params.fieldType,
	}

	// 根据字段类型进行不同的处理
	switch params.fieldType {
	case pb.EnumFieldType_STR_FIELD:
		result.SimpleValue = d.processStrField(ctx, params.value)
	case pb.EnumFieldType_INT_FIELD:
		result.SimpleValue = d.processIntField(ctx, params.value)
	case pb.EnumFieldType_FLOAT_FIELD:
		result.SimpleValue = d.processFloatField(ctx, params.value)
	case pb.EnumFieldType_TIME_FIELD:
		result.SimpleValue = d.processTimeField(ctx, params.value)
	case pb.EnumFieldType_INT_VEC_FIELD:
		result.SimpleValue = d.processIntVecField(ctx, params.value)
	case pb.EnumFieldType_SET_FIELD:
		result.SimpleValue = d.processSetField(ctx, params.value)
	case pb.EnumFieldType_MAP_KV_FIELD, pb.EnumFieldType_MAP_KLIST_FIELD:
		result.MapValue = d.processMapField(ctx, params.value, params.fieldID, params.mapKeys)
	}
	return result
}

// processStrField 处理字符串类型字段
func (d *DuckDB) processStrField(ctx context.Context, value any) *pb.SimpleValue {
	if strVal, ok := value.(string); ok {
		return &pb.SimpleValue{Value: &pb.SimpleValue_Str{Str: strVal}}
	} else if bVal, ok := value.([]byte); ok {
		return &pb.SimpleValue{Value: &pb.SimpleValue_Str{Str: string(bVal)}}
	} else if uuid, ok := value.(duckdb.UUID); ok {
		// 处理DuckDB的UUID类型
		return &pb.SimpleValue{Value: &pb.SimpleValue_Str{Str: uuid.String()}}
	} else if decimal, ok := value.(duckdb.Decimal); ok {
		// 处理DuckDB的Decimal类型，转换为字符串
		return &pb.SimpleValue{Value: &pb.SimpleValue_Str{Str: decimal.String()}}
	} else {
		// 尝试转换其他类型为字符串
		return &pb.SimpleValue{Value: &pb.SimpleValue_Str{Str: fmt.Sprintf("%v", value)}}
	}
}

// processIntField 处理整型字段
func (d *DuckDB) processIntField(ctx context.Context, value any) *pb.SimpleValue {
	var intVal int64
	if i64, ok := value.(int64); ok {
		intVal = i64
	} else if i32, ok := value.(int32); ok {
		intVal = int64(i32)
	} else if i, ok := value.(int); ok {
		intVal = int64(i)
	} else if u64, ok := value.(uint64); ok {
		intVal = int64(u64)
	} else if u32, ok := value.(uint32); ok {
		intVal = int64(u32)
	} else if decimal, ok := value.(duckdb.Decimal); ok {
		// 处理DuckDB的Decimal类型，转换为int64
		floatVal := decimal.Float64()
		intVal = int64(floatVal)
		log.DebugContextf(ctx, "Converted DuckDB Decimal %s to int64: %d", decimal.String(), intVal)
	} else if strVal, ok := value.(string); ok {
		if v, err := strconv.ParseInt(strVal, 10, 64); err == nil {
			intVal = v
		}
	} else if bVal, ok := value.([]byte); ok {
		if v, err := strconv.ParseInt(string(bVal), 10, 64); err == nil {
			intVal = v
		}
	} else {
		log.WarnContextf(ctx, "Failed to parse INT_FIELD type: %T", value)
	}
	return &pb.SimpleValue{Value: &pb.SimpleValue_Int{Int: intVal}}
}

// processFloatField 处理浮点型字段
func (d *DuckDB) processFloatField(ctx context.Context, value any) *pb.SimpleValue {
	var floatVal float64
	if f64, ok := value.(float64); ok {
		floatVal = f64
	} else if f32, ok := value.(float32); ok {
		floatVal = float64(f32)
	} else if decimal, ok := value.(duckdb.Decimal); ok {
		// 处理DuckDB的Decimal类型，转换为float64
		floatVal = decimal.Float64()
		log.DebugContextf(ctx, "Converted DuckDB Decimal %s to float64: %f", decimal.String(), floatVal)
	} else if strVal, ok := value.(string); ok {
		if v, err := strconv.ParseFloat(strVal, 64); err == nil {
			floatVal = v
		}
	} else if bVal, ok := value.([]byte); ok {
		if v, err := strconv.ParseFloat(string(bVal), 64); err == nil {
			floatVal = v
		}
	} else {
		log.WarnContextf(ctx, "Failed to parse FLOAT_FIELD type: %T", value)
	}
	return &pb.SimpleValue{Value: &pb.SimpleValue_Float{Float: floatVal}}
}

// processTimeField 处理时间字段
func (d *DuckDB) processTimeField(ctx context.Context, value any) *pb.SimpleValue {
	var timeStr string
	if strVal, ok := value.(string); ok {
		timeStr = strVal
	} else if bVal, ok := value.([]byte); ok {
		timeStr = string(bVal)
	} else if interval, ok := value.(duckdb.Interval); ok {
		// 处理DuckDB的Interval类型，转换为ISO 8601格式
		timeStr = fmt.Sprintf("P%dM%dDT%dS", interval.Months, interval.Days, interval.Micros/1000000)
		log.DebugContextf(ctx, "Converted DuckDB Interval to time string: %s", timeStr)
	} else if decimal, ok := value.(duckdb.Decimal); ok {
		// 处理DuckDB的Decimal类型，可能表示时间戳
		timeStr = decimal.String()
		log.DebugContextf(ctx, "Converted DuckDB Decimal to time string: %s", timeStr)
	} else {
		log.WarnContextf(ctx, "Failed to parse TIME_FIELD type: %T", value)
		timeStr = fmt.Sprintf("%v", value)
	}
	return &pb.SimpleValue{Value: &pb.SimpleValue_Time{Time: timeStr}}
}

// processIntVecField 处理整型向量字段
func (d *DuckDB) processIntVecField(ctx context.Context, value any) *pb.SimpleValue {
	var intList []uint32

	if strVal, ok := value.(string); ok && strVal != "" {
		if err := json.Unmarshal([]byte(strVal), &intList); err == nil {
			return d.convertUint32ToInt64List(intList)
		} else {
			log.WarnContextf(ctx, "Failed to parse INT_VEC_FIELD: %v", err)
		}
	} else if bVal, ok := value.([]byte); ok && len(bVal) > 0 {
		if err := json.Unmarshal(bVal, &intList); err == nil {
			return d.convertUint32ToInt64List(intList)
		} else {
			log.WarnContextf(ctx, "Failed to parse INT_VEC_FIELD from bytes: %v", err)
		}
	}
	return nil
}

// convertUint32ToInt64List 将uint32列表转换为int64列表
func (d *DuckDB) convertUint32ToInt64List(intList []uint32) *pb.SimpleValue {
	intValues := make([]int64, len(intList))
	for i, v := range intList {
		intValues[i] = int64(v)
	}
	return &pb.SimpleValue{Value: &pb.SimpleValue_IntList{IntList: &pb.IntList{Values: intValues}}}
}

// processSetField 处理字符串集合字段
func (d *DuckDB) processSetField(ctx context.Context, value any) *pb.SimpleValue {
	var strList []string

	if strVal, ok := value.(string); ok && strVal != "" {
		if err := json.Unmarshal([]byte(strVal), &strList); err == nil {
			return &pb.SimpleValue{Value: &pb.SimpleValue_StrList{StrList: &pb.StrList{Values: strList}}}
		} else {
			log.WarnContextf(ctx, "Failed to parse SET_FIELD: %v", err)
		}
	} else if bVal, ok := value.([]byte); ok && len(bVal) > 0 {
		if err := json.Unmarshal(bVal, &strList); err == nil {
			return &pb.SimpleValue{Value: &pb.SimpleValue_StrList{StrList: &pb.StrList{Values: strList}}}
		} else {
			log.WarnContextf(ctx, "Failed to parse SET_FIELD from bytes: %v", err)
		}
	}
	return nil
}

// processMapField 处理Map类型字段
func (d *DuckDB) processMapField(ctx context.Context, value any, fieldID uint32, mapKeys map[uint32]*pb.KeyList) *pb.MapContainer {
	var mapVal map[string]*pb.KeyValueEntry

	if strVal, ok := value.(string); ok && strVal != "" {
		if err := json.Unmarshal([]byte(strVal), &mapVal); err == nil {
			mapContainer := &pb.MapContainer{Entries: mapVal}
			return d.filterMapValuesByKeys(mapContainer, fieldID, mapKeys)
		} else {
			log.WarnContextf(ctx, "Failed to parse MAP_FIELD: %v", err)
		}
	} else if bVal, ok := value.([]byte); ok && len(bVal) > 0 {
		if err := json.Unmarshal(bVal, &mapVal); err == nil {
			mapContainer := &pb.MapContainer{Entries: mapVal}
			return d.filterMapValuesByKeys(mapContainer, fieldID, mapKeys)
		} else {
			log.WarnContextf(ctx, "Failed to parse MAP_FIELD from bytes: %v", err)
		}
	}
	return nil
}

// filterMapValuesByKeys 根据mapKeys参数过滤Map字段的值
func (d *DuckDB) filterMapValuesByKeys(mapVal *pb.MapContainer, fieldID uint32, mapKeys map[uint32]*pb.KeyList) *pb.MapContainer {
	// 如果未指定mapKeys，返回原始值
	if mapKeys == nil {
		return mapVal
	}

	// 获取字段对应的键列表
	keyList, exists := mapKeys[fieldID]
	// 如果不存在字段ID，或KeyList为空，则返回全部map值（不过滤）
	if !exists || keyList == nil || len(keyList.Keys) == 0 {
		return mapVal
	}

	// 创建只包含指定key的新Map
	filteredMap := &pb.MapContainer{
		Entries: make(map[string]*pb.KeyValueEntry),
	}
	for _, key := range keyList.Keys {
		if val, ok := mapVal.Entries[key]; ok {
			filteredMap.Entries[key] = val
		}
	}
	return filteredMap
}

// getFieldType 根据字段ID获取字段类型
func (d *DuckDB) getFieldType(ctx context.Context, fieldID uint32) (pb.EnumFieldType, error) {
	field := cache.GetFieldInfoByID(int(fieldID))
	if field == nil {
		log.ErrorContextf(ctx, "Field info not found in cache for fieldID: %d", fieldID)
		return pb.EnumFieldType_INVALID_FIELD, fmt.Errorf("field info not found in cache for fieldID: %d", fieldID)
	}
	return pb.EnumFieldType(field.FieldPrimaryFormat), nil // 字段一级格式，即字段存储类型
}

// formatColumnName 把字段ID转换为DB列名
func (d *DuckDB) formatColumnName(ctx context.Context, tableID string, fieldID uint32) string {
	field := cache.GetFieldInfoByID(int(fieldID))
	if field == nil {
		log.ErrorContextf(ctx, "Field info not found in cache for fieldID: %d", fieldID)
		return ""
	}

	tableType := helper.ParseTableType(tableID)
	if field.TableType != int(tableType) {
		log.DebugContextf(ctx, "Skip field %d for table %s: field table_type=%d, table_type=%d",
			fieldID, tableID, field.TableType, tableType)
		return ""
	}

	// 使用统一的列名映射函数
	columnName, err := dao.GetColumnByField(fieldID, tableID)
	if err != nil {
		log.DebugContextf(ctx, "Skip field %d for table %s: %v (不会导致插入失败，只会忽略该字段)", fieldID, tableID, err)
		return ""
	}
	log.DebugContextf(ctx, "formatColumnName: Column name for fieldID %d, tableID %s: %s", fieldID, tableID, columnName)
	return columnName
}

// parseFieldID 从DB列名获取字段ID
func (d *DuckDB) parseFieldID(ctx context.Context, tableID string, columnName string) uint32 {
	// 使用统一的列名映射函数
	fieldID, err := dao.GetFieldByColumn(columnName, tableID)
	if err != nil {
		log.DebugContextf(ctx, "Failed to get fieldID for column %s: tableID %s, %v", columnName, tableID, err)
		return 0
	}
	return fieldID
}

// isTableNotExistError 检查错误是否为表不存在错误
func (d *DuckDB) isTableNotExistError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	// DuckDB表不存在时的错误信息通常包含"Catalog Error"和"does not exist"
	return strings.Contains(errMsg, "Catalog Error") && strings.Contains(errMsg, "does not exist")
}
