package view

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/eventmapper"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/eventconsumer"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

func (s *Service) HandleDatasetRows(ctx context.Context, message *eventpb.EventMessage, payload *storagepb.DatasetRowsUpserted) error {
	if message == nil || payload == nil {
		return eventconsumer.Permanent(errors.New("storage dataset event is empty"))
	}
	rowEvent, err := eventmapper.ToStorageRows(payload)
	if err != nil {
		return eventconsumer.Permanent(err)
	}
	return s.applyDatasetEvent(ctx, message.GetSpaceId(), message.GetSubjectId(), rowEvent.GetRows())
}

func (s *Service) applyDatasetEvent(ctx context.Context, spaceID, datasetID string, rows []*pb.RowFieldUpsert) error {
	s.mu.RLock()
	ref := datasetRef{spaceID: spaceID, datasetID: datasetID}
	viewKeys := make(map[viewRef]struct{})
	var standalone []string
	for id := range s.byData[ref] {
		if viewKey, ok := s.indexView[id]; ok {
			viewKeys[viewKey] = struct{}{}
		} else {
			standalone = append(standalone, id)
		}
	}
	s.mu.RUnlock()
	for viewKey := range viewKeys {
		s.mu.RLock()
		runtime := s.views[viewKey]
		s.mu.RUnlock()
		if runtime == nil {
			continue
		}
		runtime.mu.Lock()
		if err := s.applyEventToIndex(ctx, runtime.active, datasetID, rows); err != nil {
			log.Printf("storage view active index write failed space=%s view=%s index=%s dataset=%s: %v", viewKey.spaceID, viewKey.viewID, runtime.active, datasetID, err)
			runtime.mu.Unlock()
			return err
		}
		if runtime.next != "" {
			if err := s.applyEventToIndex(ctx, runtime.next, datasetID, rows); err != nil {
				failedID := runtime.next
				s.failRuntimeBuild(ctx, viewKey, runtime, err)
				runtime.next = ""
				runtime.status = "failed"
				runtime.mu.Unlock()
				s.removeFailedBuild(ctx, failedID)
				continue
			}
		}
		runtime.mu.Unlock()
	}
	for _, id := range standalone {
		if err := s.applyEventToIndex(ctx, id, datasetID, rows); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyEventToIndex(ctx context.Context, id, datasetID string, rows []*pb.RowFieldUpsert) error {
	if id == "" {
		return nil
	}
	engine, err := s.engineFor(id)
	if err != nil {
		return err
	}
	s.mu.RLock()
	schema := s.schemas[id]
	s.mu.RUnlock()
	writes := eventWrites(schema, datasetID, rows)
	if len(writes) == 0 {
		return nil
	}
	complete, incomplete := partitionCompleteWrites(schema, writes)
	if len(incomplete) > 0 {
		recovered, err := s.recoverMissingRows(ctx, engine, id, schema, datasetID, rows, incomplete)
		if err != nil {
			return err
		}
		complete = append(complete, recovered...)
	}
	if len(complete) == 0 {
		return nil
	}
	return engine.Write(ctx, id, viewindex.ViewIndexWriteBatch{RowWrites: complete, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite})
}

func partitionCompleteWrites(schema viewindex.ViewIndexSchema, writes []viewindex.RowWrite) (complete, incomplete []viewindex.RowWrite) {
	required := make(map[string]struct{}, len(schema.Columns))
	for _, column := range schema.Columns {
		if column != nil && column.GetColumnName() != "" {
			required[column.GetColumnName()] = struct{}{}
		}
	}
	for _, write := range writes {
		present := make(map[string]struct{}, len(write.Fields))
		for _, field := range write.Fields {
			if field != nil {
				present[field.GetFieldId()] = struct{}{}
			}
		}
		isComplete := len(required) > 0
		for name := range required {
			if _, ok := present[name]; !ok {
				isComplete = false
				break
			}
		}
		if isComplete {
			complete = append(complete, write)
		} else {
			incomplete = append(incomplete, write)
		}
	}
	return complete, incomplete
}

func (s *Service) recoverMissingRows(ctx context.Context, _ viewindex.Engine, _ string, schema viewindex.ViewIndexSchema, datasetID string, eventRowsInput []*pb.RowFieldUpsert, writes []viewindex.RowWrite) ([]viewindex.RowWrite, error) {
	s.mu.RLock()
	reader := s.primary
	auth := s.primaryAuth
	if auth != nil {
		auth = proto.Clone(auth).(*pb.AuthInfo)
	}
	s.mu.RUnlock()
	if reader == nil || auth == nil {
		return nil, errors.New("primary reader and auth are required to recover a missing view row")
	}
	type sourceField struct{ dataset, source, target string }
	var sources []sourceField
	byDataset := make(map[string][]string)
	seen := make(map[string]map[string]struct{})
	for _, column := range schema.Columns {
		if column == nil {
			continue
		}
		sourceDataset := viewColumnDataset(column)
		origin := column.GetOriginId()
		if sourceDataset == "" || origin == "" {
			continue
		}
		if index := strings.LastIndexByte(origin, '.'); index >= 0 && index+1 < len(origin) {
			origin = origin[index+1:]
		}
		sources = append(sources, sourceField{dataset: sourceDataset, source: origin, target: column.GetColumnName()})
		if seen[sourceDataset] == nil {
			seen[sourceDataset] = make(map[string]struct{})
		}
		if _, ok := seen[sourceDataset][origin]; !ok {
			seen[sourceDataset][origin] = struct{}{}
			byDataset[sourceDataset] = append(byDataset[sourceDataset], origin)
		}
	}
	eventRows := make(map[string]*pb.RowFieldUpsert, len(eventRowsInput))
	for _, event := range eventRowsInput {
		if event == nil || event.GetKey() == nil {
			continue
		}
		key := proto.Clone(event.GetKey()).(*pb.RowKey)
		key.DatasetId = schema.PrimaryDatasetID
		eventRows[viewindex.RowKeyID(key)] = event
	}
	type sourceRows struct {
		values  map[string]*pb.RowFieldValues
		present map[string]struct{}
	}
	rowsByDataset := make(map[string]sourceRows)
	for sourceDataset, fieldIDs := range byDataset {
		keys := make([]*pb.RowKey, 0, len(writes))
		for _, write := range writes {
			key := proto.Clone(write.Key.Key).(*pb.RowKey)
			key.DatasetId = sourceDataset
			keys = append(keys, key)
		}
		rsp, err := reader.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: auth, Keys: keys, FieldIds: fieldIDs})
		if err != nil {
			return nil, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		loaded := sourceRows{values: make(map[string]*pb.RowFieldValues), present: make(map[string]struct{})}
		for _, row := range rsp.GetRows() {
			if row != nil && row.GetKey() != nil && (len(row.GetFields()) != 0 || len(row.GetAttributes()) != 0) {
				loaded.values[viewindex.RowKeyID(row.GetKey())] = row
				loaded.present[viewindex.RowKeyID(row.GetKey())] = struct{}{}
			}
		}
		for _, key := range rsp.GetExistingKeys() {
			if key != nil {
				loaded.present[viewindex.RowKeyID(key)] = struct{}{}
			}
		}
		rowsByDataset[sourceDataset] = loaded
	}
	result := make([]viewindex.RowWrite, 0, len(writes))
	for _, write := range writes {
		primaryKey := proto.Clone(write.Key.Key).(*pb.RowKey)
		primaryKey.DatasetId = schema.PrimaryDatasetID
		primaryID := viewindex.RowKeyID(primaryKey)
		if _, ok := rowsByDataset[schema.PrimaryDatasetID].present[primaryID]; !ok {
			continue
		}
		complete := viewindex.RowWrite{Key: write.Key}
		for _, source := range sources {
			var fields []*pb.FieldValue
			if row := rowsByDataset[source.dataset].values[viewindex.RowKeyID(withDataset(write.Key.Key, source.dataset))]; row != nil {
				fields = append(fields, row.GetFields()...)
			}
			if source.dataset == datasetID {
				if event := eventRows[primaryID]; event != nil {
					fields = append(fields, event.GetFields()...)
				}
			}
			complete.Fields = appendMatchingField(complete.Fields, fields, source.source, source.target)
		}
		result = append(result, complete)
	}
	return result, nil
}

func withDataset(key *pb.RowKey, datasetID string) *pb.RowKey {
	clone := proto.Clone(key).(*pb.RowKey)
	clone.DatasetId = datasetID
	return clone
}

func appendMatchingField(dst, fields []*pb.FieldValue, source, target string) []*pb.FieldValue {
	for _, field := range fields {
		if field != nil && field.GetFieldId() == source {
			value := &pb.FieldValue{FieldId: target, Value: field.GetValue()}
			for index, existing := range dst {
				if existing != nil && existing.GetFieldId() == target {
					dst[index] = value
					value = nil
					break
				}
			}
			if value != nil {
				dst = append(dst, value)
			}
		}
	}
	return dst
}

func eventWrites(schema viewindex.ViewIndexSchema, datasetID string, rows []*pb.RowFieldUpsert) []viewindex.RowWrite {
	columns := make(map[string]string)
	for _, column := range schema.Columns {
		if column == nil || viewColumnDataset(column) != datasetID {
			continue
		}
		source := viewColumnSource(column, datasetID)
		if source != "" {
			columns[source] = column.GetColumnName()
		}
	}
	if len(columns) == 0 {
		return nil
	}
	writes := make([]viewindex.RowWrite, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		fields := make([]*pb.FieldValue, 0, len(row.GetFields()))
		for _, field := range row.GetFields() {
			if name := columns[field.GetFieldId()]; name != "" {
				fields = append(fields, &pb.FieldValue{FieldId: name, Value: field.GetValue()})
			}
		}
		if len(fields) != 0 {
			key := proto.Clone(row.GetKey()).(*pb.RowKey)
			if schema.PrimaryDatasetID != "" {
				key.DatasetId = schema.PrimaryDatasetID
			}
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: key}, Fields: fields})
		}
	}
	return writes
}

func viewColumnSource(column *pb.ViewColumn, datasetID string) string {
	if column == nil {
		return ""
	}
	origin := column.GetOriginId()
	prefix := datasetID + "."
	if strings.HasPrefix(origin, prefix) {
		return strings.TrimPrefix(origin, prefix)
	}
	if idx := strings.LastIndexByte(origin, '.'); idx >= 0 && idx+1 < len(origin) {
		return origin[idx+1:]
	}
	return ""
}

func viewColumnDataset(column *pb.ViewColumn) string {
	if column == nil {
		return ""
	}
	origin := column.GetOriginId()
	if idx := strings.LastIndexByte(origin, '.'); idx > 0 {
		return origin[:idx]
	}
	return ""
}
