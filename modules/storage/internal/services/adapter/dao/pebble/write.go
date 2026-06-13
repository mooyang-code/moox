package pebble

import (
	"context"
	"encoding/json"
	"fmt"

	pebblekv "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

func (p *Pebble) processStaticDataUpdate(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
	rsp := setSuccessRsp()
	batch := p.db.NewBatch()
	defer batch.Close()

	for _, updateRow := range params.UpdateDocRows {
		rowID := updateRow.RowId
		deleted, err := p.isRowDeleted(params.TableID, rowID)
		if err != nil {
			log.ErrorContextf(ctx, "检查删除状态失败: %v", err)
			continue
		}
		if deleted {
			log.WarnContextf(ctx, "行[%s]已删除，跳过更新", rowID)
			continue
		}

		oldRows, _ := p.GetStaticFieldInfos(ctx, &dao.GetFieldParams{
			TableID:  params.TableID,
			DataType: pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
			RowID:    rowID,
		})
		var oldDocRow *pb.DocRow
		if len(oldRows) > 0 {
			oldDocRow = oldRows[0]
		}

		newDocRow := &pb.DocRow{RowId: rowID, Fields: make(map[uint32]*pb.FieldInfo)}
		for fieldID, updateFieldInfo := range updateRow.Fields {
			if updateFieldInfo == nil || updateFieldInfo.FieldInfo == nil {
				continue
			}
			key := buildFieldKey(params.TableID, rowID, "", fieldID)
			if err := p.applyFieldUpdate(batch, key, updateFieldInfo); err != nil {
				log.ErrorContextf(ctx, "更新字段失败: %v", err)
				continue
			}
			if updateFieldInfo.UpdateType == pb.EnumUpdateType_SET_UPDATE {
				newDocRow.Fields[fieldID] = updateFieldInfo.FieldInfo
			}
		}

		if oldDocRow != nil || len(newDocRow.Fields) > 0 {
			rsp.ModifyInfos = append(rsp.ModifyInfos, &pb.ModifyFieldInfo{
				OldDocRow: oldDocRow,
				NewDocRow: newDocRow,
			})
		}
	}

	if err := batch.Commit(p.writeOptions); err != nil {
		return nil, errs.New(int(pb.EnumErrorCode_FAILED_UPDATE), fmt.Sprintf("批量写入失败: %v", err))
	}
	return rsp, nil
}

func (p *Pebble) processTimingDataUpdate(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
	rsp := setSuccessRsp()
	batch := p.db.NewBatch()
	defer batch.Close()

	for _, updateRow := range params.UpdateDocRows {
		for fieldID, updateFieldInfo := range updateRow.Fields {
			if updateFieldInfo == nil || updateFieldInfo.FieldInfo == nil {
				continue
			}
			key := buildFieldKey(params.TableID, updateRow.RowId, updateRow.Times, fieldID)
			if err := p.applyFieldUpdate(batch, key, updateFieldInfo); err != nil {
				log.ErrorContextf(ctx, "更新字段失败: %v", err)
				continue
			}
		}
	}

	if err := batch.Commit(p.writeOptions); err != nil {
		return nil, errs.New(int(pb.EnumErrorCode_FAILED_UPDATE), fmt.Sprintf("批量写入失败: %v", err))
	}
	if params.HistoricalRowsLimit > 0 && len(params.UpdateDocRows) > 0 {
		firstRow := params.UpdateDocRows[0]
		lastRows, err := p.getLastNRows(ctx, params.TableID, firstRow.RowId, params.HistoricalRowsLimit)
		if err == nil {
			rsp.LastRows = lastRows
		}
	}
	return rsp, nil
}

func (p *Pebble) applyFieldUpdate(batch *pebblekv.Batch, key string, updateFieldInfo *pb.UpdateFieldInfo) error {
	switch updateFieldInfo.UpdateType {
	case pb.EnumUpdateType_SET_UPDATE:
		value, err := serializeFieldValue(updateFieldInfo.FieldInfo)
		if err != nil {
			return err
		}
		return batch.Set([]byte(key), value, nil)
	case pb.EnumUpdateType_DEL_UPDATE:
		return batch.Delete([]byte(key), nil)
	case pb.EnumUpdateType_APPEND_UPDATE:
		return p.appendFieldValue(batch, key, updateFieldInfo.FieldInfo)
	default:
		return nil
	}
}

func (p *Pebble) appendFieldValue(batch *pebblekv.Batch, key string, fieldInfo *pb.FieldInfo) error {
	data, exists, err := p.get(key)
	if err != nil {
		return err
	}
	switch fieldInfo.FieldType {
	case pb.EnumFieldType_MAP_KV_FIELD:
		existingMap := &pb.MapContainer{Entries: make(map[string]*pb.KeyValueEntry)}
		if exists {
			_ = json.Unmarshal(data, existingMap)
		}
		if fieldInfo.MapValue != nil {
			for k, v := range fieldInfo.MapValue.Entries {
				existingMap.Entries[k] = v
			}
		}
		mergedValue, err := json.Marshal(existingMap)
		if err != nil {
			return err
		}
		return batch.Set([]byte(key), mergedValue, nil)
	case pb.EnumFieldType_SET_FIELD:
		existingSet := []string{}
		if exists {
			_ = json.Unmarshal(data, &existingSet)
		}
		setMap := make(map[string]bool)
		for _, value := range existingSet {
			setMap[value] = true
		}
		for _, value := range fieldInfo.SimpleValue.GetStrList().Values {
			setMap[value] = true
		}
		mergedSet := make([]string, 0, len(setMap))
		for value := range setMap {
			mergedSet = append(mergedSet, value)
		}
		mergedValue, err := json.Marshal(mergedSet)
		if err != nil {
			return err
		}
		return batch.Set([]byte(key), mergedValue, nil)
	default:
		return fmt.Errorf("APPEND_UPDATE only supports MAP and SET types")
	}
}
