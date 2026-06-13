package pebble

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	pebblekv "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

func (p *Pebble) get(key string) ([]byte, bool, error) {
	data, closer, err := p.db.Get([]byte(key))
	if errors.Is(err, pebblekv.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer closer.Close()
	copied := append([]byte(nil), data...)
	return copied, true, nil
}

func (p *Pebble) newPrefixIter(prefix string) (*pebblekv.Iterator, error) {
	return p.db.NewIter(&pebblekv.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: prefixUpperBound([]byte(prefix)),
	})
}

func (p *Pebble) scanKeysWithPrefix(prefix string) ([][]byte, error) {
	iter, err := p.newPrefixIter(prefix)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var keys [][]byte
	for valid := iter.SeekGE([]byte(prefix)); valid; valid = iter.Next() {
		keys = append(keys, append([]byte(nil), iter.Key()...))
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return keys, nil
}

func (p *Pebble) isRowDeleted(tableID, rowID string) (bool, error) {
	data, exists, err := p.get(buildDeletedKey(tableID, rowID))
	if err != nil || !exists {
		return false, err
	}
	return string(data) == "1", nil
}

func (p *Pebble) batchCheckDeleted(tableID string, rowIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(rowIDs))
	for _, rowID := range rowIDs {
		deleted, err := p.isRowDeleted(tableID, rowID)
		if err != nil {
			return nil, err
		}
		result[rowID] = deleted
	}
	return result, nil
}

func (p *Pebble) getFieldType(ctx context.Context, fieldID uint32) (pb.EnumFieldType, error) {
	field := cache.GetFieldInfoByID(int(fieldID))
	if field == nil {
		log.ErrorContextf(ctx, "field info not found in cache for fieldID: %d", fieldID)
		return pb.EnumFieldType_INVALID_FIELD, fmt.Errorf("field info not found in cache for fieldID: %d", fieldID)
	}
	return pb.EnumFieldType(field.FieldPrimaryFormat), nil
}

func (p *Pebble) appendStaticRowIfVisible(tableID, rowID string, docRow *pb.DocRow, results *[]*pb.DocRow) bool {
	if rowID == "" {
		return false
	}
	deleted, _ := p.isRowDeleted(tableID, rowID)
	if deleted {
		return false
	}
	*results = append(*results, docRow)
	return true
}

func buildFieldKey(tableID, rowID, times string, fieldID uint32) string {
	return fmt.Sprintf("%s|%s|%s|f%d", tableID, rowID, times, fieldID)
}

func buildRowPrefix(tableID, rowID, times string) string {
	return fmt.Sprintf("%s|%s|%s|f", tableID, rowID, times)
}

func buildDeletedKey(tableID, rowID string) string {
	return fmt.Sprintf("%s|%s||_meta|deleted", tableID, rowID)
}

func buildDeletedTimeKey(tableID, rowID string) string {
	return fmt.Sprintf("%s|%s||_meta|deleted_time", tableID, rowID)
}

func buildTableMetaKey(tableID string) string {
	return fmt.Sprintf("%s|_table_meta|exists", tableID)
}

func parseFieldIDFromKey(key string) (uint32, error) {
	parts := strings.Split(key, "|")
	if len(parts) < 4 {
		return 0, fmt.Errorf("invalid key format")
	}
	fieldPart := parts[len(parts)-1]
	if !strings.HasPrefix(fieldPart, "f") {
		return 0, fmt.Errorf("invalid field prefix")
	}
	fieldID, err := strconv.ParseUint(strings.TrimPrefix(fieldPart, "f"), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse field ID failed: %v", err)
	}
	return uint32(fieldID), nil
}

func parseKeyComponents(key string) (tableID, rowID, times string, fieldID uint32, err error) {
	parts := strings.Split(key, "|")
	if len(parts) < 4 {
		err = fmt.Errorf("invalid key format: expected at least 4 parts, got %d", len(parts))
		return
	}
	tableID = parts[0]
	rowID = parts[1]
	times = parts[2]
	fieldID, err = parseFieldIDFromKey(key)
	return
}

func serializeFieldValue(fieldInfo *pb.FieldInfo) ([]byte, error) {
	switch fieldInfo.FieldType {
	case pb.EnumFieldType_STR_FIELD:
		return []byte(fieldInfo.SimpleValue.GetStr()), nil
	case pb.EnumFieldType_INT_FIELD:
		return []byte(fmt.Sprintf("%d", fieldInfo.SimpleValue.GetInt())), nil
	case pb.EnumFieldType_FLOAT_FIELD:
		return []byte(fmt.Sprintf("%f", fieldInfo.SimpleValue.GetFloat())), nil
	case pb.EnumFieldType_TIME_FIELD:
		return []byte(fieldInfo.SimpleValue.GetTime()), nil
	case pb.EnumFieldType_INT_VEC_FIELD:
		return json.Marshal(fieldInfo.SimpleValue.GetIntList().Values)
	case pb.EnumFieldType_SET_FIELD:
		return json.Marshal(fieldInfo.SimpleValue.GetStrList().Values)
	case pb.EnumFieldType_MAP_KV_FIELD:
		return json.Marshal(fieldInfo.MapValue)
	default:
		return nil, fmt.Errorf("unsupported field type: %v", fieldInfo.FieldType)
	}
}

func deserializeFieldValue(data []byte, fieldType pb.EnumFieldType) (*pb.FieldInfo, error) {
	fieldInfo := &pb.FieldInfo{FieldType: fieldType}
	switch fieldType {
	case pb.EnumFieldType_STR_FIELD:
		fieldInfo.SimpleValue = &pb.SimpleValue{Value: &pb.SimpleValue_Str{Str: string(data)}}
	case pb.EnumFieldType_INT_FIELD:
		intVal, err := strconv.ParseInt(string(data), 10, 64)
		if err != nil {
			return nil, err
		}
		fieldInfo.SimpleValue = &pb.SimpleValue{Value: &pb.SimpleValue_Int{Int: intVal}}
	case pb.EnumFieldType_FLOAT_FIELD:
		floatVal, err := strconv.ParseFloat(string(data), 64)
		if err != nil {
			return nil, err
		}
		fieldInfo.SimpleValue = &pb.SimpleValue{Value: &pb.SimpleValue_Float{Float: floatVal}}
	case pb.EnumFieldType_TIME_FIELD:
		fieldInfo.SimpleValue = &pb.SimpleValue{Value: &pb.SimpleValue_Time{Time: string(data)}}
	case pb.EnumFieldType_INT_VEC_FIELD:
		var values []int64
		if err := json.Unmarshal(data, &values); err != nil {
			return nil, err
		}
		fieldInfo.SimpleValue = &pb.SimpleValue{Value: &pb.SimpleValue_IntList{IntList: &pb.IntList{Values: values}}}
	case pb.EnumFieldType_SET_FIELD:
		var values []string
		if err := json.Unmarshal(data, &values); err != nil {
			return nil, err
		}
		fieldInfo.SimpleValue = &pb.SimpleValue{Value: &pb.SimpleValue_StrList{StrList: &pb.StrList{Values: values}}}
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

func filterRowsMapFields(rows []*pb.DocRow, mapKeys map[uint32]*pb.KeyList) {
	if len(mapKeys) == 0 {
		return
	}
	for _, row := range rows {
		filterMapFields(row, mapKeys)
	}
}

func filterMapFields(docRow *pb.DocRow, mapKeys map[uint32]*pb.KeyList) {
	if docRow == nil || len(mapKeys) == 0 {
		return
	}
	for fieldID, keyList := range mapKeys {
		fieldInfo := docRow.Fields[fieldID]
		if fieldInfo == nil || fieldInfo.FieldType != pb.EnumFieldType_MAP_KV_FIELD {
			continue
		}
		filteredEntries := make(map[string]*pb.KeyValueEntry)
		for _, key := range keyList.Keys {
			if entry, ok := fieldInfo.MapValue.GetEntries()[key]; ok {
				filteredEntries[key] = entry
			}
		}
		fieldInfo.MapValue.Entries = filteredEntries
	}
}

func contains(slice []uint32, item uint32) bool {
	for _, value := range slice {
		if value == item {
			return true
		}
	}
	return false
}

func prefixUpperBound(prefix []byte) []byte {
	if len(prefix) == 0 {
		return nil
	}
	upper := append([]byte(nil), prefix...)
	for i := len(upper) - 1; i >= 0; i-- {
		if upper[i] != 0xff {
			upper[i]++
			return upper[:i+1]
		}
	}
	return nil
}

func successRetInfo() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.EnumErrorCode_SUCCESS, Msg: "success"}
}

func setSuccessRsp() *pb.SetFieldInfosRsp {
	return &pb.SetFieldInfosRsp{
		RetInfo:     successRetInfo(),
		ModifyInfos: []*pb.ModifyFieldInfo{},
		LastRows:    []*pb.DocRow{},
		FailedRows:  []*pb.FailedDocRow{},
	}
}

func bytesCompare(a, b []byte) int {
	return bytes.Compare(a, b)
}
