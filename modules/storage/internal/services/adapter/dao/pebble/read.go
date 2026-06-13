package pebble

import (
	"context"
	"fmt"

	pebblekv "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

func (p *Pebble) GetStaticFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
	if params.RowID != "" {
		docRow, err := p.readStaticRow(ctx, params.TableID, params.RowID, params.FieldIDs, params.MapKeys)
		if err != nil || docRow == nil {
			return []*pb.DocRow{}, err
		}
		return []*pb.DocRow{docRow}, nil
	}

	var results []*pb.DocRow
	prefix := params.TableID + "|"
	iter, err := p.newPrefixIter(prefix)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	currentRowID := ""
	currentDocRow := &pb.DocRow{Fields: make(map[uint32]*pb.FieldInfo)}
	count := uint32(0)
	for valid := iter.SeekGE([]byte(prefix)); valid; valid = iter.Next() {
		key := string(iter.Key())
		_, parsedRowID, times, fieldID, err := parseKeyComponents(key)
		if err != nil || times != "" {
			continue
		}
		if parsedRowID != currentRowID {
			if p.appendStaticRowIfVisible(params.TableID, currentRowID, currentDocRow, &results) {
				count++
				if params.MaxLimit > 0 && count >= params.MaxLimit {
					break
				}
			}
			currentRowID = parsedRowID
			currentDocRow = &pb.DocRow{RowId: parsedRowID, Fields: make(map[uint32]*pb.FieldInfo)}
		}
		if len(params.FieldIDs) > 0 && !contains(params.FieldIDs, fieldID) {
			continue
		}
		if err := p.addFieldFromIterator(ctx, iter, fieldID, currentDocRow); err != nil {
			log.WarnContextf(ctx, "反序列化字段失败: fieldID=%d, err=%v", fieldID, err)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	if currentRowID != "" && (params.MaxLimit == 0 || count < params.MaxLimit) {
		p.appendStaticRowIfVisible(params.TableID, currentRowID, currentDocRow, &results)
	}
	filterRowsMapFields(results, params.MapKeys)
	return results, nil
}

func (p *Pebble) GetTimingFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
	if params.TimeInterval == nil || params.TimeInterval.Start == "" {
		return nil, fmt.Errorf("时序数据查询必须指定时间范围")
	}

	startKey := buildRowPrefix(params.TableID, params.RowID, params.TimeInterval.Start)
	end := params.TimeInterval.End
	if end == "" {
		end = "9999-12-31 23:59:59"
	}
	endKey := buildRowPrefix(params.TableID, params.RowID, end) + "~"

	iter, err := p.db.NewIter(&pebblekv.IterOptions{
		LowerBound: []byte(startKey),
		UpperBound: []byte(endKey),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []*pb.DocRow
	currentTimes := ""
	currentDocRow := &pb.DocRow{Fields: make(map[uint32]*pb.FieldInfo)}
	count := uint32(0)
	for valid := iter.SeekGE([]byte(startKey)); valid; valid = iter.Next() {
		_, parsedRowID, times, fieldID, err := parseKeyComponents(string(iter.Key()))
		if err != nil || times == "" {
			continue
		}
		if params.RowID != "" && parsedRowID != params.RowID {
			continue
		}
		if times != currentTimes {
			if currentTimes != "" {
				results = append(results, currentDocRow)
				count++
				if params.MaxLimit > 0 && count >= params.MaxLimit {
					break
				}
			}
			currentTimes = times
			currentDocRow = &pb.DocRow{RowId: parsedRowID, Times: times, Fields: make(map[uint32]*pb.FieldInfo)}
		}
		if len(params.FieldIDs) > 0 && !contains(params.FieldIDs, fieldID) {
			continue
		}
		if err := p.addFieldFromIterator(ctx, iter, fieldID, currentDocRow); err != nil {
			log.WarnContextf(ctx, "反序列化字段失败: fieldID=%d, err=%v", fieldID, err)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	if currentTimes != "" && (params.MaxLimit == 0 || count < params.MaxLimit) {
		results = append(results, currentDocRow)
	}
	filterRowsMapFields(results, params.MapKeys)
	return results, nil
}

func (p *Pebble) readStaticRow(ctx context.Context, tableID, rowID string, fieldIDs []uint32,
	mapKeys map[uint32]*pb.KeyList) (*pb.DocRow, error) {
	deleted, err := p.isRowDeleted(tableID, rowID)
	if err != nil || deleted {
		return nil, err
	}
	docRow := &pb.DocRow{RowId: rowID, Fields: make(map[uint32]*pb.FieldInfo)}
	if len(fieldIDs) > 0 {
		for _, fieldID := range fieldIDs {
			data, exists, err := p.get(buildFieldKey(tableID, rowID, "", fieldID))
			if err != nil {
				return nil, err
			}
			if !exists {
				continue
			}
			if err := p.addFieldValue(ctx, data, fieldID, docRow); err != nil {
				log.WarnContextf(ctx, "反序列化字段失败: fieldID=%d, err=%v", fieldID, err)
			}
		}
	} else {
		prefix := buildRowPrefix(tableID, rowID, "")
		iter, err := p.newPrefixIter(prefix)
		if err != nil {
			return nil, err
		}
		defer iter.Close()
		for valid := iter.SeekGE([]byte(prefix)); valid; valid = iter.Next() {
			fieldID, err := parseFieldIDFromKey(string(iter.Key()))
			if err != nil {
				continue
			}
			if err := p.addFieldFromIterator(ctx, iter, fieldID, docRow); err != nil {
				log.WarnContextf(ctx, "反序列化字段失败: fieldID=%d, err=%v", fieldID, err)
			}
		}
		if err := iter.Error(); err != nil {
			return nil, err
		}
	}
	filterMapFields(docRow, mapKeys)
	return docRow, nil
}

func (p *Pebble) getLastNRows(ctx context.Context, tableID, rowID string, limit uint32) ([]*pb.DocRow, error) {
	prefix := buildRowPrefix(tableID, rowID, "")
	iter, err := p.newPrefixIter(prefix)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []*pb.DocRow
	currentTimes := ""
	currentDocRow := &pb.DocRow{Fields: make(map[uint32]*pb.FieldInfo)}
	count := uint32(0)
	for valid := iter.Last(); valid && count < limit; valid = iter.Prev() {
		_, _, times, fieldID, err := parseKeyComponents(string(iter.Key()))
		if err != nil || times == "" {
			continue
		}
		if times != currentTimes {
			if currentTimes != "" {
				results = append([]*pb.DocRow{currentDocRow}, results...)
				count++
				if count >= limit {
					break
				}
			}
			currentTimes = times
			currentDocRow = &pb.DocRow{RowId: rowID, Times: times, Fields: make(map[uint32]*pb.FieldInfo)}
		}
		if err := p.addFieldFromIterator(ctx, iter, fieldID, currentDocRow); err != nil {
			log.WarnContextf(ctx, "反序列化字段失败: fieldID=%d, err=%v", fieldID, err)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	if currentTimes != "" && count < limit {
		results = append([]*pb.DocRow{currentDocRow}, results...)
	}
	return results, nil
}

func (p *Pebble) addFieldFromIterator(ctx context.Context, iter *pebblekv.Iterator, fieldID uint32,
	docRow *pb.DocRow) error {
	return p.addFieldValue(ctx, append([]byte(nil), iter.Value()...), fieldID, docRow)
}

func (p *Pebble) addFieldValue(ctx context.Context, data []byte, fieldID uint32, docRow *pb.DocRow) error {
	fieldType, err := p.getFieldType(ctx, fieldID)
	if err != nil {
		return err
	}
	fieldInfo, err := deserializeFieldValue(data, fieldType)
	if err != nil {
		return err
	}
	fieldInfo.FieldId = fieldID
	docRow.Fields[fieldID] = fieldInfo
	return nil
}
