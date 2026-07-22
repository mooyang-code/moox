package view

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/view/eventconsumer"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

func (s *Service) StartEventConsumer(ctx context.Context, client *jetstream.Client) (func(), error) {
	if client == nil {
		return nil, errors.New("eventbus client is required")
	}
	consumer, err := client.EnsurePullConsumer(ctx, jetstream.ConsumerConfig{Stream: "MOOX_STORAGE", Durable: "storage_view", FilterSubject: eventconsumer.DatasetFieldsChangedSubjectPrefix + ".>", AckWait: 120 * time.Second, MaxDeliver: -1, MaxAckPending: 1, FetchMaxWait: time.Second})
	if err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer consumer.Close()
		for loopCtx.Err() == nil {
			deliveries, fetchErr := consumer.Fetch(loopCtx, 1)
			if fetchErr != nil {
				if loopCtx.Err() != nil {
					return
				}
				timer := time.NewTimer(250 * time.Millisecond)
				select {
				case <-timer.C:
				case <-loopCtx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				}
				continue
			}
			for _, delivery := range deliveries {
				s.processDelivery(loopCtx, delivery)
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

func (s *Service) processDelivery(ctx context.Context, delivery *jetstream.Delivery) {
	s.liveWork.Add(1)
	defer s.liveWork.Add(-1)
	if err := s.acquireLiveDelivery(ctx, delivery); err != nil {
		if ctx.Err() == nil {
			log.Printf("storage view delivery gate failed: %v", err)
		}
		return
	}
	defer s.releaseLiveGate()
	for ctx.Err() == nil {
		err := s.applyDelivery(ctx, delivery)
		if err == nil {
			if ackErr := delivery.Ack(ctx); ackErr != nil {
				log.Printf("storage view delivery ack failed: %v", ackErr)
			}
			return
		}
		if isPermanentDeliveryError(err) {
			if termErr := delivery.Term(ctx); termErr != nil {
				log.Printf("storage view delivery term failed after permanent error %v: %v", err, termErr)
			}
			return
		}
		// Keep the delivery pending while retrying. NAK would release
		// MaxAckPending and allow a later event to overtake it.
		if progressErr := delivery.InProgress(ctx); progressErr != nil {
			log.Printf("storage view delivery progress failed after %v: %v", err, progressErr)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		}
	}
}

func (s *Service) initLiveGate() {
	s.liveGateOnce.Do(func() {
		s.liveGate = make(chan struct{}, 1)
		s.liveGate <- struct{}{}
	})
}

func (s *Service) acquireBackfill(ctx context.Context) error {
	s.initLiveGate()
	select {
	case <-s.liveGate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) acquireLiveDelivery(ctx context.Context, delivery *jetstream.Delivery) error {
	s.initLiveGate()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.liveGate:
			return nil
		case <-ticker.C:
			if delivery != nil {
				_ = delivery.InProgress(ctx)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Service) releaseLiveGate() {
	s.liveGate <- struct{}{}
}

func (s *Service) applyDelivery(ctx context.Context, delivery *jetstream.Delivery) error {
	if delivery == nil || delivery.Message == nil {
		if delivery != nil && delivery.DecodeError != nil {
			return permanentDeliveryError{delivery.DecodeError}
		}
		return permanentDeliveryError{errors.New("storage event delivery is empty")}
	}
	spaceID, datasetID, err := jetstream.ValidateStorageFieldsChangedEnvelope(delivery.Message)
	if err != nil {
		return permanentDeliveryError{err}
	}
	subjectSpaceID, subjectDatasetID, err := eventconsumer.ParseDatasetFieldsChangedSubject("", delivery.Subject)
	if err != nil {
		return permanentDeliveryError{err}
	}
	if subjectSpaceID != spaceID || subjectDatasetID != datasetID {
		return permanentDeliveryError{errors.New("storage delivery subject and envelope topic mismatch")}
	}
	event := &pb.DatasetFieldsChanged{}
	if err := proto.Unmarshal(delivery.Message.GetPayload(), event); err != nil {
		return permanentDeliveryError{err}
	}
	if event.GetSpaceId() != spaceID || event.GetDatasetId() != datasetID {
		return permanentDeliveryError{errors.New("dataset event subject and payload mismatch")}
	}
	return s.applyDatasetEvent(ctx, spaceID, datasetID, event.GetRows())
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
	needsRecovery := datasetID != schema.PrimaryDatasetID
	if !needsRecovery {
		needsRecovery, err = s.hasMissingRows(ctx, engine, id, writes)
		if err != nil {
			return err
		}
	}
	if needsRecovery {
		writes, err = s.recoverMissingRows(ctx, engine, id, schema, datasetID, rows, writes)
		if err != nil {
			return err
		}
		if len(writes) == 0 {
			return nil
		}
	}
	mode := viewindex.LiveWrite
	if needsRecovery {
		mode = viewindex.Replace
	}
	return engine.Write(ctx, id, viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: mode})
}

func (s *Service) hasMissingRows(ctx context.Context, engine viewindex.Engine, id string, writes []viewindex.RowWrite) (bool, error) {
	keys := make([]*pb.RowKey, 0, len(writes))
	for _, write := range writes {
		keys = append(keys, write.Key.Key)
	}
	rows, _, err := engine.Query(ctx, id, viewindex.QuerySpec{Keys: keys, TotalMode: pb.TotalMode_NONE})
	if err != nil {
		return false, err
	}
	present := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row != nil && row.GetKey() != nil {
			present[viewindex.RowKeyID(row.GetKey())] = struct{}{}
		}
	}
	for _, key := range keys {
		if _, ok := present[viewindex.RowKeyID(key)]; !ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) recoverMissingRows(ctx context.Context, _ viewindex.Engine, _ string, schema viewindex.ViewIndexSchema, datasetID string, events []*pb.RowFieldUpsert, writes []viewindex.RowWrite) ([]viewindex.RowWrite, error) {
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
	eventRows := make(map[string]*pb.RowFieldUpsert, len(events))
	for _, event := range events {
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

type permanentDeliveryError struct{ error }

func isPermanentDeliveryError(err error) bool {
	var target permanentDeliveryError
	return errors.As(err, &target)
}
