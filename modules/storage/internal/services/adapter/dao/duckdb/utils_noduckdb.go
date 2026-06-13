//go:build noduckdb || !cgo
// +build noduckdb !cgo

package duckdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/helper"
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

type RowProcessor func(docRow *pb.DocRow, columns []string, values []any)

func (d *DuckDB) executeQueryWithRowProcessor(ctx context.Context, query string, args []any,
	mapKeys map[uint32]*pb.KeyList, rowProcessor RowProcessor) ([]*pb.DocRow, error) {
	log.DebugContextf(ctx, "执行查询SQL: %s, args: %v", query, args)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		if d.isTableNotExistError(err) {
			log.WarnContextf(ctx, "Table does not exist, returning empty result: %v", err)
			return []*pb.DocRow{}, nil
		}
		return nil, errs.New(int(pb.EnumErrorCode_FAILED_SELECT), fmt.Sprintf("Failed to execute query: %v", err))
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, errs.New(int(pb.EnumErrorCode_FAILED_SELECT), fmt.Sprintf("Failed to get column names: %v", err))
	}

	docRows := make([]*pb.DocRow, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			log.ErrorContextf(ctx, "Failed to scan row: %v", err)
			continue
		}

		docRow := &pb.DocRow{Fields: make(map[uint32]*pb.FieldInfo)}
		if rowProcessor != nil {
			rowProcessor(docRow, columns, values)
		}
		for i, col := range columns {
			if col == "_row_id" || col == "_times" {
				continue
			}
			fieldID := d.parseFieldID(ctx, d.tableID, col)
			if fieldID == 0 {
				continue
			}
			fieldType, err := d.getFieldType(ctx, fieldID)
			if err != nil {
				continue
			}
			docRow.Fields[fieldID] = d.transFieldValue(ctx, &fieldValueParams{
				fieldID:   fieldID,
				fieldType: fieldType,
				value:     values[i],
				mapKeys:   mapKeys,
			})
		}
		docRows = append(docRows, docRow)
	}
	return docRows, nil
}

func (d *DuckDB) buildColumnNameList(fieldIDs []uint32) string {
	if d.isGetAll {
		return "*"
	}

	seen := make(map[string]bool)
	colList := make([]string, 0, len(constants.SystemFields)+len(fieldIDs))
	for field := range constants.SystemFields {
		if !seen[field] {
			colList = append(colList, field)
			seen[field] = true
		}
	}
	for _, fieldID := range fieldIDs {
		columnName := d.formatColumnName(context.Background(), d.tableID, fieldID)
		if columnName != "" && !constants.SystemFields[columnName] && !seen[columnName] {
			colList = append(colList, columnName)
			seen[columnName] = true
		}
	}
	if len(colList) == 0 {
		return "*"
	}
	return strings.Join(colList, ", ")
}

type fieldValueParams struct {
	fieldID   uint32
	fieldType pb.EnumFieldType
	value     any
	mapKeys   map[uint32]*pb.KeyList
}

func (d *DuckDB) transFieldValue(ctx context.Context, params *fieldValueParams) *pb.FieldInfo {
	result := &pb.FieldInfo{
		FieldId:   params.fieldID,
		FieldType: params.fieldType,
	}
	if params.value == nil {
		return result
	}

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

func (d *DuckDB) processStrField(_ context.Context, value any) *pb.SimpleValue {
	if bVal, ok := value.([]byte); ok {
		return &pb.SimpleValue{Value: &pb.SimpleValue_Str{Str: string(bVal)}}
	}
	return &pb.SimpleValue{Value: &pb.SimpleValue_Str{Str: fmt.Sprintf("%v", value)}}
}

func (d *DuckDB) processIntField(ctx context.Context, value any) *pb.SimpleValue {
	var intVal int64
	switch v := value.(type) {
	case int64:
		intVal = v
	case int32:
		intVal = int64(v)
	case int:
		intVal = int64(v)
	case uint64:
		intVal = int64(v)
	case uint32:
		intVal = int64(v)
	case string:
		intVal, _ = strconv.ParseInt(v, 10, 64)
	case []byte:
		intVal, _ = strconv.ParseInt(string(v), 10, 64)
	default:
		log.WarnContextf(ctx, "Failed to parse INT_FIELD type: %T", value)
	}
	return &pb.SimpleValue{Value: &pb.SimpleValue_Int{Int: intVal}}
}

func (d *DuckDB) processFloatField(ctx context.Context, value any) *pb.SimpleValue {
	var floatVal float64
	switch v := value.(type) {
	case float64:
		floatVal = v
	case float32:
		floatVal = float64(v)
	case string:
		floatVal, _ = strconv.ParseFloat(v, 64)
	case []byte:
		floatVal, _ = strconv.ParseFloat(string(v), 64)
	default:
		log.WarnContextf(ctx, "Failed to parse FLOAT_FIELD type: %T", value)
	}
	return &pb.SimpleValue{Value: &pb.SimpleValue_Float{Float: floatVal}}
}

func (d *DuckDB) processTimeField(ctx context.Context, value any) *pb.SimpleValue {
	switch v := value.(type) {
	case string:
		return &pb.SimpleValue{Value: &pb.SimpleValue_Time{Time: v}}
	case []byte:
		return &pb.SimpleValue{Value: &pb.SimpleValue_Time{Time: string(v)}}
	default:
		log.WarnContextf(ctx, "Failed to parse TIME_FIELD type: %T", value)
		return &pb.SimpleValue{Value: &pb.SimpleValue_Time{Time: fmt.Sprintf("%v", value)}}
	}
}

func (d *DuckDB) processIntVecField(ctx context.Context, value any) *pb.SimpleValue {
	raw, ok := rawJSON(value)
	if !ok {
		log.WarnContextf(ctx, "Failed to parse INT_VEC_FIELD type: %T", value)
		return nil
	}
	var intList []uint32
	if err := json.Unmarshal(raw, &intList); err != nil {
		log.WarnContextf(ctx, "Failed to parse INT_VEC_FIELD: %v", err)
		return nil
	}
	return d.convertUint32ToInt64List(intList)
}

func (d *DuckDB) convertUint32ToInt64List(intList []uint32) *pb.SimpleValue {
	intValues := make([]int64, len(intList))
	for i, v := range intList {
		intValues[i] = int64(v)
	}
	return &pb.SimpleValue{Value: &pb.SimpleValue_IntList{IntList: &pb.IntList{Values: intValues}}}
}

func (d *DuckDB) processSetField(ctx context.Context, value any) *pb.SimpleValue {
	raw, ok := rawJSON(value)
	if !ok {
		log.WarnContextf(ctx, "Failed to parse SET_FIELD type: %T", value)
		return nil
	}
	var strList []string
	if err := json.Unmarshal(raw, &strList); err != nil {
		log.WarnContextf(ctx, "Failed to parse SET_FIELD: %v", err)
		return nil
	}
	return &pb.SimpleValue{Value: &pb.SimpleValue_StrList{StrList: &pb.StrList{Values: strList}}}
}

func (d *DuckDB) processMapField(ctx context.Context, value any, fieldID uint32, mapKeys map[uint32]*pb.KeyList) *pb.MapContainer {
	raw, ok := rawJSON(value)
	if !ok {
		log.WarnContextf(ctx, "Failed to parse MAP_FIELD type: %T", value)
		return nil
	}
	var mapVal map[string]*pb.KeyValueEntry
	if err := json.Unmarshal(raw, &mapVal); err != nil {
		log.WarnContextf(ctx, "Failed to parse MAP_FIELD: %v", err)
		return nil
	}
	return d.filterMapValuesByKeys(&pb.MapContainer{Entries: mapVal}, fieldID, mapKeys)
}

func (d *DuckDB) filterMapValuesByKeys(mapVal *pb.MapContainer, fieldID uint32, mapKeys map[uint32]*pb.KeyList) *pb.MapContainer {
	if mapKeys == nil {
		return mapVal
	}
	keyList, exists := mapKeys[fieldID]
	if !exists || keyList == nil || len(keyList.Keys) == 0 {
		return mapVal
	}
	filteredMap := &pb.MapContainer{Entries: make(map[string]*pb.KeyValueEntry)}
	for _, key := range keyList.Keys {
		if val, ok := mapVal.Entries[key]; ok {
			filteredMap.Entries[key] = val
		}
	}
	return filteredMap
}

func (d *DuckDB) getFieldType(ctx context.Context, fieldID uint32) (pb.EnumFieldType, error) {
	field := cache.GetFieldInfoByID(int(fieldID))
	if field == nil {
		log.ErrorContextf(ctx, "Field info not found in cache for fieldID: %d", fieldID)
		return pb.EnumFieldType_INVALID_FIELD, fmt.Errorf("field info not found in cache for fieldID: %d", fieldID)
	}
	return pb.EnumFieldType(field.FieldPrimaryFormat), nil
}

func (d *DuckDB) formatColumnName(ctx context.Context, tableID string, fieldID uint32) string {
	field := cache.GetFieldInfoByID(int(fieldID))
	if field == nil {
		log.ErrorContextf(ctx, "Field info not found in cache for fieldID: %d", fieldID)
		return ""
	}
	tableType := helper.ParseTableType(tableID)
	if field.TableType != int(tableType) {
		return ""
	}
	columnName, err := dao.GetColumnByField(fieldID, tableID)
	if err != nil {
		log.DebugContextf(ctx, "Skip field %d for table %s: %v", fieldID, tableID, err)
		return ""
	}
	return columnName
}

func (d *DuckDB) parseFieldID(ctx context.Context, tableID string, columnName string) uint32 {
	fieldID, err := dao.GetFieldByColumn(columnName, tableID)
	if err != nil {
		log.DebugContextf(ctx, "Failed to get fieldID for column %s: tableID %s, %v", columnName, tableID, err)
		return 0
	}
	return fieldID
}

func (d *DuckDB) isTableNotExistError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "Catalog Error") && strings.Contains(errMsg, "does not exist")
}

func rawJSON(value any) ([]byte, bool) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil, false
		}
		return []byte(v), true
	case []byte:
		return v, len(v) > 0
	default:
		return nil, false
	}
}
