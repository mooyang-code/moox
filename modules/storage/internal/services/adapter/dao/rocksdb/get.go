//go:build !norocksdb && cgo
// +build !norocksdb,cgo

// Package rocksdb duckdb相关逻辑
package rocksdb

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// GetFieldInfos 统一获取数据接口，RocksDB支持静态数据和时序数据存储
func (r *RocksDB) GetFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
	r.tableID = params.TableID

	// 根据 data_type 参数优先判断数据类型
	switch params.DataType {
	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		// 直接处理为时序数据
		return r.GetTimingFieldInfos(ctx, params)
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		// 直接处理为静态数据
		return r.GetStaticFieldInfos(ctx, params)
	default:
		// 处理默认情况
		return nil, fmt.Errorf("invalid data type")
	}
}

// GetStaticFieldInfos 静态数据：统一获取value接口
func (r *RocksDB) GetStaticFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
	tableID := params.TableID
	rowID := params.RowID
	fieldIDs := params.FieldIDs
	mapKeys := params.MapKeys
	maxLimit := params.MaxLimit

	var results []*pb.DocRow

	// 情况1：查询指定行
	if rowID != "" {
		docRow, err := r.readStaticRow(ctx, tableID, rowID, fieldIDs, mapKeys)
		if err != nil {
			return nil, err
		}
		if docRow != nil {
			results = append(results, docRow)
		}
		return results, nil
	}

	// 情况2：查询所有行
	tablePrefix := fmt.Sprintf("%s|", tableID)
	it := r.db.NewIterator(r.ro)
	defer it.Close()

	currentRowID := ""
	currentDocRow := &pb.DocRow{Fields: make(map[uint32]*pb.FieldInfo)}
	count := uint32(0)

	for it.Seek([]byte(tablePrefix)); it.ValidForPrefix([]byte(tablePrefix)); it.Next() {
		key := string(it.Key().Data())

		// 解析 Key
		_, parsedRowID, times, fieldID, err := parseKeyComponents(key)
		if err != nil || times != "" { // 静态数据 times 必须为空
			continue
		}

		// 新行开始
		if parsedRowID != currentRowID {
			// 保存上一行（检查删除标记）
			if currentRowID != "" {
				deleted, _ := r.isRowDeleted(tableID, currentRowID)
				if !deleted {
					results = append(results, currentDocRow)
					count++
					if maxLimit > 0 && count >= maxLimit {
						break
					}
				}
			}

			// 开始新行
			currentRowID = parsedRowID
			currentDocRow = &pb.DocRow{
				RowId:  parsedRowID,
				Fields: make(map[uint32]*pb.FieldInfo),
			}
		}

		// 过滤字段（如果指定了 fieldIDs）
		if len(fieldIDs) > 0 && !contains(fieldIDs, fieldID) {
			continue
		}

		// 反序列化字段值
		fieldType, err := r.getFieldType(ctx, fieldID)
		if err != nil {
			log.WarnContextf(ctx, "获取字段类型失败: fieldID=%d, err=%v", fieldID, err)
			continue
		}

		fieldInfo, err := deserializeFieldValue(it.Value().Data(), fieldType)
		if err != nil {
			log.WarnContextf(ctx, "反序列化字段值失败: fieldID=%d, err=%v", fieldID, err)
			continue
		}

		fieldInfo.FieldId = fieldID
		currentDocRow.Fields[fieldID] = fieldInfo
	}

	// 处理最后一行
	if currentRowID != "" {
		deleted, _ := r.isRowDeleted(tableID, currentRowID)
		if !deleted && (maxLimit == 0 || count < maxLimit) {
			results = append(results, currentDocRow)
		}
	}

	// 过滤 Map 字段
	if len(mapKeys) > 0 {
		for _, docRow := range results {
			filterMapFields(docRow, mapKeys)
		}
	}

	return results, nil
}

// readStaticRow 读取单行静态数据（辅助函数）
func (r *RocksDB) readStaticRow(ctx context.Context, tableID, rowID string,
	fieldIDs []uint32, mapKeys map[uint32]*pb.KeyList) (*pb.DocRow, error) {

	// 检查删除标记
	deleted, err := r.isRowDeleted(tableID, rowID)
	if err != nil {
		return nil, err
	}
	if deleted {
		return nil, nil // 已删除，返回空
	}

	docRow := &pb.DocRow{
		RowId:  rowID,
		Fields: make(map[uint32]*pb.FieldInfo),
	}

	// 如果指定了字段列表，使用 MultiGet
	if len(fieldIDs) > 0 {
		keys := make([][]byte, len(fieldIDs))
		for i, fieldID := range fieldIDs {
			key := buildFieldKey(tableID, rowID, "", fieldID)
			keys[i] = []byte(key)
		}

		values, err := r.db.MultiGet(r.ro, keys...)
		if err != nil {
			return nil, err
		}

		for i, val := range values {
			defer val.Free()
			if !val.Exists() {
				continue
			}

			fieldID := fieldIDs[i]
			fieldType, err := r.getFieldType(ctx, fieldID)
			if err != nil {
				continue
			}

			fieldInfo, err := deserializeFieldValue(val.Data(), fieldType)
			if err != nil {
				continue
			}

			fieldInfo.FieldId = fieldID
			docRow.Fields[fieldID] = fieldInfo
		}
	} else {
		// 扫描所有字段
		rowPrefix := buildRowPrefix(tableID, rowID, "")
		it := r.db.NewIterator(r.ro)
		defer it.Close()

		for it.Seek([]byte(rowPrefix)); it.ValidForPrefix([]byte(rowPrefix)); it.Next() {
			key := string(it.Key().Data())
			fieldID, err := parseFieldIDFromKey(key)
			if err != nil {
				continue
			}

			fieldType, err := r.getFieldType(ctx, fieldID)
			if err != nil {
				continue
			}

			fieldInfo, err := deserializeFieldValue(it.Value().Data(), fieldType)
			if err != nil {
				continue
			}

			fieldInfo.FieldId = fieldID
			docRow.Fields[fieldID] = fieldInfo
		}
	}

	// 过滤 Map 字段
	if len(mapKeys) > 0 {
		filterMapFields(docRow, mapKeys)
	}

	return docRow, nil
}

// GetTimingFieldInfos 时序数据：统一获取value接口
func (r *RocksDB) GetTimingFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
	tableID := params.TableID
	rowID := params.RowID
	timeInterval := params.TimeInterval
	fieldIDs := params.FieldIDs
	mapKeys := params.MapKeys
	maxLimit := params.MaxLimit

	// 时序数据必须指定时间范围
	if timeInterval == nil || timeInterval.Start == "" {
		return nil, fmt.Errorf("时序数据查询必须指定时间范围")
	}

	// 构建扫描范围
	startKey := buildRowPrefix(tableID, rowID, timeInterval.Start)
	// endKey 需要包含所有该时间点的字段，所以使用 "~" 作为结束符（ASCII 126，在所有字母数字之后）
	endKey := buildRowPrefix(tableID, rowID, timeInterval.End) + "~"
	if timeInterval.End == "" {
		endKey = buildRowPrefix(tableID, rowID, "9999-12-31 23:59:59") + "~" // 最大时间
	}

	var results []*pb.DocRow
	it := r.db.NewIterator(r.ro)
	defer it.Close()

	currentTimes := ""
	currentDocRow := &pb.DocRow{Fields: make(map[uint32]*pb.FieldInfo)}
	count := uint32(0)

	for it.Seek([]byte(startKey)); it.Valid(); it.Next() {
		key := string(it.Key().Data())

		// 检查是否超出范围
		if key >= endKey {
			break
		}

		// 解析 Key
		_, parsedRowID, times, fieldID, err := parseKeyComponents(key)
		if err != nil || times == "" { // 时序数据 times 不能为空
			continue
		}

		// 如果指定了 rowID，必须匹配
		if rowID != "" && parsedRowID != rowID {
			continue
		}

		// 新时间点开始
		if times != currentTimes {
			// 保存上一时间点（时序数据不检查删除标记）
			if currentTimes != "" {
				results = append(results, currentDocRow)
				count++
				if maxLimit > 0 && count >= maxLimit {
					break
				}
			}

			// 开始新时间点
			currentTimes = times
			currentDocRow = &pb.DocRow{
				RowId:  parsedRowID,
				Times:  times,
				Fields: make(map[uint32]*pb.FieldInfo),
			}
		}

		// 过滤字段
		if len(fieldIDs) > 0 && !contains(fieldIDs, fieldID) {
			continue
		}

		// 反序列化字段值
		fieldType, err := r.getFieldType(ctx, fieldID)
		if err != nil {
			continue
		}

		fieldInfo, err := deserializeFieldValue(it.Value().Data(), fieldType)
		if err != nil {
			continue
		}

		fieldInfo.FieldId = fieldID
		currentDocRow.Fields[fieldID] = fieldInfo
	}

	// 处理最后一个时间点
	if currentTimes != "" && (maxLimit == 0 || count < maxLimit) {
		results = append(results, currentDocRow)
	}

	// 过滤 Map 字段
	if len(mapKeys) > 0 {
		for _, docRow := range results {
			filterMapFields(docRow, mapKeys)
		}
	}

	return results, nil
}
