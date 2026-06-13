//go:build !norocksdb && cgo
// +build !norocksdb,cgo

package rocksdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/linxGnu/grocksdb"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// SetFieldInfos 统一更新数据接口(支持静态数据和时序数据的更新)
func (r *RocksDB) SetFieldInfos(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
	log.DebugContextf(ctx, "RocksDB SetFieldInfos: %+v", params)

	// 初始化响应
	rsp := &pb.SetFieldInfosRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		ModifyInfos: []*pb.ModifyFieldInfo{},
		LastRows:    []*pb.DocRow{},
		FailedRows:  []*pb.FailedDocRow{},
	}

	if len(params.UpdateDocRows) == 0 {
		return rsp, nil
	}

	r.tableID = params.TableID

	// 根据数据类型分发
	switch params.DataType {
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		return r.processStaticDataUpdate(ctx, params)

	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		return r.processTimingDataUpdate(ctx, params)

	default:
		return nil, fmt.Errorf("invalid data type: %v", params.DataType)
	}
}

// processStaticDataUpdate 静态数据更新处理
func (r *RocksDB) processStaticDataUpdate(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
	tableID := params.TableID
	updateDocRows := params.UpdateDocRows

	rsp := &pb.SetFieldInfosRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		ModifyInfos: []*pb.ModifyFieldInfo{},
		FailedRows:  []*pb.FailedDocRow{},
	}

	batch := grocksdb.NewWriteBatch()
	defer batch.Destroy()

	for _, updateRow := range updateDocRows {
		rowID := updateRow.RowId

		// 检查是否已删除
		deleted, err := r.isRowDeleted(tableID, rowID)
		if err != nil {
			log.ErrorContextf(ctx, "检查删除状态失败: %v", err)
			continue
		}
		if deleted {
			log.WarnContextf(ctx, "行[%s]已删除，跳过更新", rowID)
			continue
		}

		// 读取现有值（用于生成 ModifyInfos）
		getParams := &dao.GetFieldParams{
			TableID:  tableID,
			DataType: pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
			RowID:    rowID,
		}
		oldRows, _ := r.GetStaticFieldInfos(ctx, getParams)
		var oldDocRow *pb.DocRow
		if len(oldRows) > 0 {
			oldDocRow = oldRows[0]
		}

		newDocRow := &pb.DocRow{
			RowId:  rowID,
			Fields: make(map[uint32]*pb.FieldInfo),
		}

		// 处理每个字段
		for fieldID, updateFieldInfo := range updateRow.Fields {
			fieldInfo := updateFieldInfo.FieldInfo
			updateType := updateFieldInfo.UpdateType

			key := buildFieldKey(tableID, rowID, "", fieldID)

			switch updateType {
			case pb.EnumUpdateType_SET_UPDATE:
				// 覆盖写入
				value, err := serializeFieldValue(fieldInfo)
				if err != nil {
					log.ErrorContextf(ctx, "序列化字段失败: %v", err)
					continue
				}
				batch.Put([]byte(key), value)
				newDocRow.Fields[fieldID] = fieldInfo

			case pb.EnumUpdateType_DEL_UPDATE:
				// 删除字段
				batch.Delete([]byte(key))

			case pb.EnumUpdateType_APPEND_UPDATE:
				// 追加写入（MAP/SET 类型）
				err := r.appendFieldValue(batch, key, fieldInfo)
				if err != nil {
					log.ErrorContextf(ctx, "追加字段失败: %v", err)
					continue
				}
			}
		}

		// 生成 ModifyFieldInfo
		if oldDocRow != nil || len(newDocRow.Fields) > 0 {
			modifyInfo := &pb.ModifyFieldInfo{
				OldDocRow: oldDocRow,
				NewDocRow: newDocRow,
			}
			rsp.ModifyInfos = append(rsp.ModifyInfos, modifyInfo)
		}
	}

	// 提交批量写入
	err := r.db.Write(r.wo, batch)
	if err != nil {
		return nil, errs.New(int(pb.EnumErrorCode_FAILED_UPDATE), fmt.Sprintf("批量写入失败: %v", err))
	}

	log.InfoContextf(ctx, "静态数据更新成功，共 %d 行", len(updateDocRows))
	return rsp, nil
}

// processTimingDataUpdate 时序数据更新处理
func (r *RocksDB) processTimingDataUpdate(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
	tableID := params.TableID
	updateDocRows := params.UpdateDocRows
	historicalRowsLimit := params.HistoricalRowsLimit

	rsp := &pb.SetFieldInfosRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		ModifyInfos: []*pb.ModifyFieldInfo{},
		LastRows:    []*pb.DocRow{},
		FailedRows:  []*pb.FailedDocRow{},
	}

	batch := grocksdb.NewWriteBatch()
	defer batch.Destroy()

	for _, updateRow := range updateDocRows {
		rowID := updateRow.RowId
		times := updateRow.Times

		// 时序数据不检查删除标记，直接写入
		newDocRow := &pb.DocRow{
			RowId:  rowID,
			Times:  times,
			Fields: make(map[uint32]*pb.FieldInfo),
		}

		// 处理每个字段
		for fieldID, updateFieldInfo := range updateRow.Fields {
			fieldInfo := updateFieldInfo.FieldInfo
			updateType := updateFieldInfo.UpdateType

			key := buildFieldKey(tableID, rowID, times, fieldID)

			switch updateType {
			case pb.EnumUpdateType_SET_UPDATE:
				value, err := serializeFieldValue(fieldInfo)
				if err != nil {
					log.ErrorContextf(ctx, "序列化字段失败: %v", err)
					continue
				}
				batch.Put([]byte(key), value)
				newDocRow.Fields[fieldID] = fieldInfo

			case pb.EnumUpdateType_DEL_UPDATE:
				batch.Delete([]byte(key))

			case pb.EnumUpdateType_APPEND_UPDATE:
				err := r.appendFieldValue(batch, key, fieldInfo)
				if err != nil {
					log.ErrorContextf(ctx, "追加字段失败: %v", err)
					continue
				}
			}
		}

		// 简化：时序数据不生成 ModifyInfos（性能优化）
	}

	// 提交批量写入
	err := r.db.Write(r.wo, batch)
	if err != nil {
		return nil, errs.New(int(pb.EnumErrorCode_FAILED_UPDATE), fmt.Sprintf("批量写入失败: %v", err))
	}

	// 获取历史数据
	if historicalRowsLimit > 0 && len(updateDocRows) > 0 {
		firstRow := updateDocRows[0]
		lastRows, err := r.getLastNRows(ctx, tableID, firstRow.RowId, historicalRowsLimit)
		if err != nil {
			log.WarnContextf(ctx, "获取历史数据失败: %v", err)
		} else {
			rsp.LastRows = lastRows
		}
	}

	log.InfoContextf(ctx, "时序数据更新成功，共 %d 行", len(updateDocRows))
	return rsp, nil
}

// getLastNRows 获取最近 N 条时序数据
func (r *RocksDB) getLastNRows(ctx context.Context, tableID, rowID string, limit uint32) ([]*pb.DocRow, error) {
	// 构建行前缀
	rowPrefix := fmt.Sprintf("%s:%s:", tableID, rowID)

	// 倒序扫描
	it := r.db.NewIterator(r.ro)
	defer it.Close()

	var results []*pb.DocRow
	currentTimes := ""
	currentDocRow := &pb.DocRow{Fields: make(map[uint32]*pb.FieldInfo)}
	count := uint32(0)

	// 从最后开始扫描
	it.SeekToLast()

	for ; it.Valid() && count < limit; it.Prev() {
		key := string(it.Key().Data())

		// 检查前缀
		if !strings.HasPrefix(key, rowPrefix) {
			continue
		}

		// 解析 Key
		_, _, times, fieldID, err := parseKeyComponents(key)
		if err != nil || times == "" {
			continue
		}

		// 新时间点
		if times != currentTimes {
			if currentTimes != "" {
				results = append([]*pb.DocRow{currentDocRow}, results...) // 头部插入
				count++
				if count >= limit {
					break
				}
			}
			currentTimes = times
			currentDocRow = &pb.DocRow{
				RowId:  rowID,
				Times:  times,
				Fields: make(map[uint32]*pb.FieldInfo),
			}
		}

		// 反序列化字段
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

	// 最后一个时间点
	if currentTimes != "" && count < limit {
		results = append([]*pb.DocRow{currentDocRow}, results...)
	}

	return results, nil
}
