//go:build !norocksdb && cgo
// +build !norocksdb,cgo

// Package rocksdb RocksDB的工具函数
package rocksdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/linxGnu/grocksdb"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// ============================================================================
// Key 构建函数
// ============================================================================

// buildFieldKey 构建字段 Key
// 格式: {tableID}|{rowID}|{times}|f{fieldID}
func buildFieldKey(tableID, rowID, times string, fieldID uint32) string {
	return fmt.Sprintf("%s|%s|%s|f%d", tableID, rowID, times, fieldID)
}

// buildRowPrefix 构建行前缀（用于扫描某行的所有字段）
// 格式: {tableID}|{rowID}|{times}|f
func buildRowPrefix(tableID, rowID, times string) string {
	return fmt.Sprintf("%s|%s|%s|f", tableID, rowID, times)
}

// buildDeletedKey 构建删除标记 Key（仅静态数据）
// 格式: {tableID}|{rowID}||_meta|deleted
func buildDeletedKey(tableID, rowID string) string {
	return fmt.Sprintf("%s|%s||_meta|deleted", tableID, rowID)
}

// buildDeletedTimeKey 构建删除时间 Key（仅静态数据）
// 格式: {tableID}|{rowID}||_meta|deleted_time
func buildDeletedTimeKey(tableID, rowID string) string {
	return fmt.Sprintf("%s|%s||_meta|deleted_time", tableID, rowID)
}

// buildTableMetaKey 构建表元数据 Key
// 格式: {tableID}|_table_meta|exists
func buildTableMetaKey(tableID string) string {
	return fmt.Sprintf("%s|_table_meta|exists", tableID)
}

// ============================================================================
// Key 解析函数
// ============================================================================

// parseFieldIDFromKey 从 Key 中解析字段 ID
// 输入: "t_stock_kline|000001.SZ|2024-01-15 09:30:00|f123"
// 输出: 123
func parseFieldIDFromKey(key string) (uint32, error) {
	parts := strings.Split(key, "|")
	if len(parts) < 4 {
		return 0, fmt.Errorf("invalid key format")
	}

	fieldPart := parts[len(parts)-1] // 最后一段，如 "f123"
	if !strings.HasPrefix(fieldPart, "f") {
		return 0, fmt.Errorf("invalid field prefix")
	}

	fieldIDStr := strings.TrimPrefix(fieldPart, "f")
	fieldID, err := strconv.ParseUint(fieldIDStr, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse field ID failed: %v", err)
	}

	return uint32(fieldID), nil
}

// parseKeyComponents 解析 Key 的各个组成部分
// 输入: "t_stock_kline|000001.SZ|2024-01-15 09:30:00|f1"
// 输出: tableID="t_stock_kline", rowID="000001.SZ", times="2024-01-15 09:30:00", fieldID=1
func parseKeyComponents(key string) (tableID, rowID, times string, fieldID uint32, err error) {
	// Key 格式: {tableID}|{rowID}|{times}|{fieldID}
	// 使用竖线 | 作为分隔符，避免与时间字符串中的冒号冲突

	parts := strings.Split(key, "|")
	if len(parts) < 4 {
		err = fmt.Errorf("invalid key format: expected at least 4 parts, got %d", len(parts))
		return
	}

	tableID = parts[0]
	rowID = parts[1]
	times = parts[2]

	// 解析 fieldID
	fieldID, err = parseFieldIDFromKey(key)
	return
}

// ============================================================================
// 序列化和反序列化函数
// ============================================================================

// serializeFieldValue 序列化字段值
func serializeFieldValue(fieldInfo *pb.FieldInfo) ([]byte, error) {
	// 根据字段类型序列化
	switch fieldInfo.FieldType {
	case pb.EnumFieldType_STR_FIELD:
		return []byte(fieldInfo.SimpleValue.GetStr()), nil

	case pb.EnumFieldType_INT_FIELD:
		intVal := fieldInfo.SimpleValue.GetInt()
		return []byte(fmt.Sprintf("%d", intVal)), nil

	case pb.EnumFieldType_FLOAT_FIELD:
		floatVal := fieldInfo.SimpleValue.GetFloat()
		return []byte(fmt.Sprintf("%f", floatVal)), nil

	case pb.EnumFieldType_TIME_FIELD:
		return []byte(fieldInfo.SimpleValue.GetTime()), nil

	case pb.EnumFieldType_INT_VEC_FIELD:
		// JSON 序列化
		return json.Marshal(fieldInfo.SimpleValue.GetIntList().Values)

	case pb.EnumFieldType_SET_FIELD:
		// JSON 序列化
		return json.Marshal(fieldInfo.SimpleValue.GetStrList().Values)

	case pb.EnumFieldType_MAP_KV_FIELD:
		// JSON 序列化 Map
		return json.Marshal(fieldInfo.MapValue)

	default:
		return nil, fmt.Errorf("unsupported field type: %v", fieldInfo.FieldType)
	}
}

// deserializeFieldValue 反序列化字段值
func deserializeFieldValue(data []byte, fieldType pb.EnumFieldType) (*pb.FieldInfo, error) {
	fieldInfo := &pb.FieldInfo{
		FieldType: fieldType,
	}

	switch fieldType {
	case pb.EnumFieldType_STR_FIELD:
		fieldInfo.SimpleValue = &pb.SimpleValue{
			Value: &pb.SimpleValue_Str{Str: string(data)},
		}

	case pb.EnumFieldType_INT_FIELD:
		intVal, err := strconv.ParseInt(string(data), 10, 64)
		if err != nil {
			return nil, err
		}
		fieldInfo.SimpleValue = &pb.SimpleValue{
			Value: &pb.SimpleValue_Int{Int: intVal},
		}

	case pb.EnumFieldType_FLOAT_FIELD:
		floatVal, err := strconv.ParseFloat(string(data), 64)
		if err != nil {
			return nil, err
		}
		fieldInfo.SimpleValue = &pb.SimpleValue{
			Value: &pb.SimpleValue_Float{Float: floatVal},
		}

	case pb.EnumFieldType_TIME_FIELD:
		fieldInfo.SimpleValue = &pb.SimpleValue{
			Value: &pb.SimpleValue_Time{Time: string(data)},
		}

	case pb.EnumFieldType_INT_VEC_FIELD:
		var values []int64
		if err := json.Unmarshal(data, &values); err != nil {
			return nil, err
		}
		fieldInfo.SimpleValue = &pb.SimpleValue{
			Value: &pb.SimpleValue_IntList{
				IntList: &pb.IntList{Values: values},
			},
		}

	case pb.EnumFieldType_SET_FIELD:
		var values []string
		if err := json.Unmarshal(data, &values); err != nil {
			return nil, err
		}
		fieldInfo.SimpleValue = &pb.SimpleValue{
			Value: &pb.SimpleValue_StrList{
				StrList: &pb.StrList{Values: values},
			},
		}

	case pb.EnumFieldType_MAP_KV_FIELD:
		var mapValue pb.MapContainer
		if err := json.Unmarshal(data, &mapValue); err != nil {
			return nil, err
		}
		fieldInfo.MapValue = &mapValue

	default:
		return nil, fmt.Errorf("unsupported field type: %v", fieldType)
	}

	return fieldInfo, nil
}

// ============================================================================
// 删除标记检查函数（仅静态数据）
// ============================================================================

// isRowDeleted 检查行是否被软删除（仅静态数据）
func (r *RocksDB) isRowDeleted(tableID, rowID string) (bool, error) {
	key := buildDeletedKey(tableID, rowID)
	value, err := r.db.Get(r.ro, []byte(key))
	if err != nil {
		return false, err
	}
	defer value.Free()

	if !value.Exists() {
		return false, nil
	}

	return string(value.Data()) == "1", nil
}

// batchCheckDeleted 批量检查多个行是否被删除（优化性能）
func (r *RocksDB) batchCheckDeleted(tableID string, rowIDs []string) (map[string]bool, error) {
	keys := make([][]byte, len(rowIDs))
	for i, rowID := range rowIDs {
		keys[i] = []byte(buildDeletedKey(tableID, rowID))
	}

	values, err := r.db.MultiGet(r.ro, keys...)
	if err != nil {
		return nil, err
	}

	result := make(map[string]bool)
	for i, val := range values {
		defer val.Free()
		result[rowIDs[i]] = val.Exists() && string(val.Data()) == "1"
	}

	return result, nil
}

// ============================================================================
// 字段类型获取函数
// ============================================================================

// getFieldType 获取字段类型（从缓存或配置中心）
func (r *RocksDB) getFieldType(ctx context.Context, fieldID uint32) (pb.EnumFieldType, error) {
	field := cache.GetFieldInfoByID(int(fieldID))
	if field == nil {
		log.ErrorContextf(ctx, "Field info not found in cache for fieldID: %d", fieldID)
		return pb.EnumFieldType_INVALID_FIELD, fmt.Errorf("field info not found in cache for fieldID: %d", fieldID)
	}
	return pb.EnumFieldType(field.FieldPrimaryFormat), nil // 字段一级格式，即字段存储类型
}

// ============================================================================
// Map 字段过滤函数
// ============================================================================

// filterMapFields 根据 mapKeys 过滤 DocRow 中的 Map 字段
func filterMapFields(docRow *pb.DocRow, mapKeys map[uint32]*pb.KeyList) {
	if docRow == nil || len(mapKeys) == 0 {
		return
	}

	for fieldID, keyList := range mapKeys {
		fieldInfo, exists := docRow.Fields[fieldID]
		if !exists || fieldInfo.FieldType != pb.EnumFieldType_MAP_KV_FIELD {
			continue
		}

		if fieldInfo.MapValue == nil || len(fieldInfo.MapValue.Entries) == 0 {
			continue
		}

		// 过滤 Map 字段，只保留指定的 keys
		if len(keyList.Keys) > 0 {
			filteredEntries := make(map[string]*pb.KeyValueEntry)
			for _, key := range keyList.Keys {
				if entry, ok := fieldInfo.MapValue.Entries[key]; ok {
					filteredEntries[key] = entry
				}
			}
			fieldInfo.MapValue.Entries = filteredEntries
		}
	}
}

// ============================================================================
// 辅助工具函数
// ============================================================================

// contains 检查 slice 中是否包含指定元素
func contains(slice []uint32, item uint32) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

// appendFieldValue 追加字段值（MAP/SET 类型）
func (r *RocksDB) appendFieldValue(batch *grocksdb.WriteBatch, key string, fieldInfo *pb.FieldInfo) error {
	// 读取现有值
	existingValue, err := r.db.Get(r.ro, []byte(key))
	if err != nil {
		return err
	}
	defer existingValue.Free()

	// 根据字段类型处理
	switch fieldInfo.FieldType {
	case pb.EnumFieldType_MAP_KV_FIELD:
		// MAP 追加
		existingMap := &pb.MapContainer{Entries: make(map[string]*pb.KeyValueEntry)}
		if existingValue.Exists() {
			json.Unmarshal(existingValue.Data(), existingMap)
		}

		// 合并新 Map
		if fieldInfo.MapValue != nil {
			for k, v := range fieldInfo.MapValue.Entries {
				existingMap.Entries[k] = v
			}
		}

		// 序列化并写入
		mergedValue, _ := json.Marshal(existingMap)
		batch.Put([]byte(key), mergedValue)

	case pb.EnumFieldType_SET_FIELD:
		// SET 追加
		existingSet := []string{}
		if existingValue.Exists() {
			json.Unmarshal(existingValue.Data(), &existingSet)
		}

		// 合并新 SET（去重）
		newSet := fieldInfo.SimpleValue.GetStrList().Values
		setMap := make(map[string]bool)
		for _, v := range existingSet {
			setMap[v] = true
		}
		for _, v := range newSet {
			setMap[v] = true
		}

		mergedSet := make([]string, 0, len(setMap))
		for k := range setMap {
			mergedSet = append(mergedSet, k)
		}

		mergedValue, _ := json.Marshal(mergedSet)
		batch.Put([]byte(key), mergedValue)

	default:
		return fmt.Errorf("APPEND_UPDATE only supports MAP and SET types")
	}

	return nil
}
